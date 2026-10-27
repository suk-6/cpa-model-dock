// Package store persists validated policy state. A Store serializes all revisions
// within this process; callers must stop the previous plugin instance before
// opening a second writer for the same directory.
package store

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
)

const HistoryLimit = 1000
const maxRecordBytes = 16 << 20

var (
	ErrRevisionConflict = errors.New("revision_conflict")
	ErrUnavailable      = errors.New("policy_unavailable")
	ErrInvalidState     = errors.New("invalid_state")
	ErrEntryNotFound    = errors.New("history_entry_not_found")
	ErrInvalidScope     = errors.New("invalid_restore_scope")
)

type Entry struct {
	ID      string       `json:"id"`
	At      time.Time    `json:"at"`
	Actor   string       `json:"actor"`
	Kind    string       `json:"kind"`
	Summary string       `json:"summary"`
	Before  policy.State `json:"before"`
	After   policy.State `json:"after"`
}

type Store struct {
	mu      sync.Mutex
	dir     string
	state   policy.State
	valid   bool
	loadErr error
	history []Entry // oldest first; only entries committed by the loaded state
}

// Open creates the directory, but never replaces corrupt policy with defaults.
// A corrupt policy is reported by Load, leaving management able to repair it.
func Open(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("state directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.loadHistory(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Load() (policy.State, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.state), s.valid, s.loadErr
}

func (s *Store) Current() (policy.State, bool) {
	state, valid, _ := s.Load()
	return state, valid
}

func (s *Store) Save(expected int64, state policy.State, actor, kind, summary string) (policy.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(expected, state, actor, kind, summary)
}

func (s *Store) save(expected int64, state policy.State, actor, kind, summary string) (policy.State, error) {
	if expected != s.state.Revision {
		return clone(s.state), ErrRevisionConflict
	}
	if s.state.Revision == math.MaxInt64 {
		return clone(s.state), fmt.Errorf("%w: revision exhausted", ErrInvalidState)
	}
	if state.Schema != 1 || (actor != "user" && actor != "auto") || (kind != "state" && kind != "alias_target") {
		return clone(s.state), ErrInvalidState
	}
	next := clone(state)
	next.Revision = s.state.Revision + 1
	next.UpdatedAt = time.Now().UTC()
	// Canonical arrays prevent a nil slice becoming a structurally incomplete
	// state file; domain validation belongs to management/policy.
	if next.Families == nil {
		next.Families = []policy.Family{}
	}
	if next.Aliases == nil {
		next.Aliases = []policy.Alias{}
	}
	if next.Rules == nil {
		next.Rules = []policy.Rule{}
	}
	data, err := json.Marshal(next)
	if err != nil {
		return clone(s.state), err
	}
	if len(data) > maxRecordBytes/2 {
		return clone(s.state), ErrInvalidState
	}
	entry := Entry{ID: strconv.FormatInt(next.Revision, 10), At: next.UpdatedAt, Actor: actor, Kind: kind, Summary: summary, Before: clone(s.state), After: next}
	record, err := json.Marshal(entry)
	if err != nil {
		return clone(s.state), err
	}
	if len(record) >= maxRecordBytes {
		return clone(s.state), ErrInvalidState
	}

	// Preserve the in-memory last good state, including after backup recovery.
	// Never overwrite a healthy backup with corrupt primary bytes.
	if s.valid {
		previous, err := json.Marshal(s.state)
		if err != nil {
			return clone(s.state), err
		}
		if err := s.atomicWrite("state.json.bak", previous); err != nil {
			return clone(s.state), err
		}
	}
	// History is a write-ahead record. On restart, entries ahead of the loaded
	// policy revision are discarded, so a crash cannot restore an uncommitted save.
	newHistory := append(append([]Entry(nil), s.history...), entry)
	if len(newHistory) > HistoryLimit {
		newHistory = newHistory[len(newHistory)-HistoryLimit:]
	}
	if err := s.writeHistory(record, newHistory); err != nil {
		return clone(s.state), err
	}
	if err := s.atomicWrite("state.json", data); err != nil {
		// Best effort rollback of the WAL. loadHistory also removes an orphan
		// after a crash or rollback failure.
		_ = s.compactHistory(s.history)
		return clone(s.state), err
	}
	s.state, s.valid, s.loadErr, s.history = next, true, nil, newHistory
	return clone(next), nil
}

// History returns newest first. before is an exclusive entry ID cursor; an
// unknown cursor returns an empty page. Returned states never share mutable data.
func (s *Store) History(limit int, before string) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = 100
	}
	if limit > HistoryLimit {
		limit = HistoryLimit
	}
	end := len(s.history)
	if before != "" {
		end = 0
		for i := range s.history {
			if s.history[i].ID == before {
				end = i
				break
			}
		}
	}
	result := make([]Entry, 0, min(limit, end))
	for i := end - 1; i >= 0 && len(result) < limit; i-- {
		entry := s.history[i]
		entry.Before = clone(entry.Before)
		entry.After = clone(entry.After)
		result = append(result, entry)
	}
	return result
}

// Restore reapplies the selected entry's After snapshot as a new user revision.
// alias_target restores only targets changed by that entry, preserving all other
// current settings, aliases and rules. Removed aliases are not resurrected.
func (s *Store) Restore(expected int64, entryID, scope string) (policy.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.state.Revision {
		return clone(s.state), ErrRevisionConflict
	}
	if scope != "state" && scope != "alias_target" {
		return clone(s.state), ErrInvalidScope
	}
	for _, entry := range s.history {
		if entry.ID != entryID {
			continue
		}
		next := clone(entry.After)
		if scope == "alias_target" {
			next = clone(s.state)
			before := make(map[string]string)
			for _, alias := range entry.Before.Aliases {
				before[alias.ID] = alias.Target
			}
			changed := make(map[string]string)
			for _, alias := range entry.After.Aliases {
				if old, exists := before[alias.ID]; exists && old != alias.Target {
					changed[alias.ID] = alias.Target
				}
			}
			for i := range next.Aliases {
				if target, exists := changed[next.Aliases[i].ID]; exists {
					next.Aliases[i].Target = target
				}
			}
		}
		return s.save(expected, next, "user", scope, "Restored history entry "+entryID)
	}
	return clone(s.state), ErrEntryNotFound
}

func (s *Store) load() error {
	exists := false
	for _, name := range []string{"state.json", "state.json.bak"} {
		data, err := readLimited(filepath.Join(s.dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		exists = true
		if err != nil {
			if !errors.Is(err, ErrInvalidState) {
				return err
			}
			continue
		}
		state, err := decodeState(data)
		if err == nil {
			s.state, s.valid = state, true
			return nil
		}
	}
	s.state = policy.DefaultState()
	if exists {
		s.state.Settings.WhitelistEnabled = true
		s.valid, s.loadErr = false, ErrUnavailable
	} else {
		s.valid = true
	}
	return nil
}

func readLimited(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRecordBytes {
		return nil, ErrInvalidState
	}
	return data, nil
}

// A syntactically valid JSON object such as {} must not disable policy on load.
// Check schema and mandatory structural fields here; policy validates semantics.
func decodeState(data []byte) (policy.State, error) {
	var state policy.State
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return state, ErrInvalidState
	}
	for _, name := range []string{"schema", "revision", "updated_at", "settings", "families", "aliases", "rules"} {
		if value, ok := fields[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return state, ErrInvalidState
		}
	}
	if json.Unmarshal(data, &state) != nil || state.Schema != 1 || state.Revision < 0 || (state.Revision > 0 && state.UpdatedAt.IsZero()) {
		return state, ErrInvalidState
	}
	var settings map[string]json.RawMessage
	if json.Unmarshal(fields["settings"], &settings) != nil || settings == nil {
		return state, ErrInvalidState
	}
	for _, name := range []string{"whitelist_enabled", "rewrite_response_model", "refresh_interval_seconds"} {
		value, ok := settings[name]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return state, ErrInvalidState
		}
	}
	for _, name := range []string{"families", "aliases", "rules"} {
		var entries []json.RawMessage
		if json.Unmarshal(fields[name], &entries) != nil {
			return state, ErrInvalidState
		}
		for _, value := range entries {
			if len(bytes.TrimSpace(value)) == 0 || bytes.TrimSpace(value)[0] != '{' {
				return state, ErrInvalidState
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(value, &object) != nil {
				return state, ErrInvalidState
			}
			required := map[string][]string{
				"families": {"id", "name", "template", "allow_dated"},
				"aliases":  {"id", "name", "enabled", "mode", "target", "expose_original"},
				"rules":    {"id", "enabled", "kind", "value"},
			}[name]
			for _, field := range required {
				if value, ok := object[field]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
					return state, ErrInvalidState
				}
			}
		}
	}
	// Domain corruption also falls back to the backup. Runtime construction
	// checks static structure without requiring a catalog during startup.
	if !policy.New(state, policy.Catalog{}, true).Available() {
		return state, ErrInvalidState
	}
	return state, nil
}

func (s *Store) loadHistory() error {
	f, err := os.Open(filepath.Join(s.dir, "history.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		s.history = []Entry{}
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), maxRecordBytes)
	dirty := false
	for scanner.Scan() {
		var entry Entry
		line := scanner.Bytes()
		if json.Unmarshal(line, &entry) != nil || entry.ID != strconv.FormatInt(entry.After.Revision, 10) || entry.At.IsZero() || (entry.Actor != "user" && entry.Actor != "auto") || (entry.Kind != "state" && entry.Kind != "alias_target") || entry.After.Revision > s.state.Revision || entry.After.Revision != entry.Before.Revision+1 || (len(s.history) > 0 && entry.After.Revision <= s.history[len(s.history)-1].After.Revision) {
			dirty = true
			continue
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(line, &fields)
		if _, err := decodeState(fields["before"]); err != nil {
			dirty = true
			continue
		}
		if _, err := decodeState(fields["after"]); err != nil {
			dirty = true
			continue
		}
		s.history = append(s.history, entry)
		if len(s.history) > HistoryLimit {
			s.history = s.history[1:]
			dirty = true
		}
	}
	if scanner.Err() != nil {
		dirty = true
	}
	// Close before replacing for platforms which disallow renaming open files.
	if err := f.Close(); err != nil {
		return err
	}
	if dirty {
		return s.compactHistory(s.history)
	}
	return nil
}

func (s *Store) writeHistory(record []byte, next []Entry) error {
	if len(s.history) >= HistoryLimit {
		return s.compactHistory(next)
	}
	f, err := os.OpenFile(filepath.Join(s.dir, "history.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(record, '\n'))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = s.compactHistory(s.history)
	}
	return err
}

func (s *Store) compactHistory(entries []Entry) error {
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	return s.atomicWrite("history.jsonl", data.Bytes())
}

func (s *Store) atomicWrite(name string, data []byte) error {
	f, err := os.CreateTemp(s.dir, ".model-dock-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(tmp, filepath.Join(s.dir, name)); err != nil {
		return err
	}
	dir, err := os.Open(s.dir)
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return err
}

func clone(state policy.State) policy.State {
	state.Families = append([]policy.Family{}, state.Families...)
	state.Aliases = append([]policy.Alias{}, state.Aliases...)
	state.Rules = append([]policy.Rule{}, state.Rules...)
	for i := range state.Aliases {
		if value := state.Aliases[i].RewriteResponseModel; value != nil {
			copy := *value
			state.Aliases[i].RewriteResponseModel = &copy
		}
	}
	return state
}
