package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
)

func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func stateOf(t *testing.T, s *Store) policy.State {
	t.Helper()
	state, valid, err := s.Load()
	if err != nil || !valid {
		t.Fatalf("Load: valid=%v err=%v", valid, err)
	}
	return state
}

func saveState(t *testing.T, s *Store, state policy.State) policy.State {
	t.Helper()
	next, err := s.Save(state.Revision, state, "user", "state", "test change")
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func TestSaveReloadHistoryAndCopies(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	state := stateOf(t, s)
	if state.Schema != 1 || state.Revision != 0 || state.Settings.WhitelistEnabled {
		t.Fatalf("wrong defaults: %+v", state)
	}
	rewrite := true
	state.Aliases = []policy.Alias{{ID: "al_1", Name: "sol", Enabled: true, Mode: "pinned", Target: "gpt-6-sol", RewriteResponseModel: &rewrite}}
	first := saveState(t, s, state)
	if first.Revision != 1 || first.UpdatedAt.IsZero() {
		t.Fatal("save did not allocate revision/timestamp")
	}
	state.Aliases[0].Name = "mutated input"
	rewrite = false
	first.Aliases[0].Name = "mutated result"
	if current := stateOf(t, s); current.Aliases[0].Name != "sol" || !*current.Aliases[0].RewriteResponseModel {
		t.Fatal("save retains caller-owned mutable data")
	}
	second := stateOf(t, s)
	second.Settings.WhitelistEnabled = true
	second = saveState(t, s, second)
	if second.Revision != 2 {
		t.Fatal(second.Revision)
	}
	reloaded := openStore(t, dir)
	if got := stateOf(t, reloaded); got.Revision != 2 || !got.Settings.WhitelistEnabled {
		t.Fatalf("reload: %+v", got)
	}
	history := reloaded.History(1, "")
	if len(history) != 1 || history[0].After.Revision != 2 || history[0].Before.Revision != 1 {
		t.Fatalf("history: %+v", history)
	}
	older := reloaded.History(10, history[0].ID)
	if len(older) != 1 || older[0].After.Revision != 1 {
		t.Fatal("bad exclusive history cursor")
	}
	if len(reloaded.History(10, "unknown")) != 0 {
		t.Fatal("unknown cursor should be empty")
	}
	history[0].After.Aliases[0].Name = "mutated history"
	if reloaded.History(1, "")[0].After.Aliases[0].Name != "sol" {
		t.Fatal("mutable history")
	}
	if _, err := reloaded.Save(1, second, "user", "state", ""); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("conflict sentinel: %v", err)
	}
	for _, name := range []string{"state.json", "state.json.bak", "history.jsonl"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("%s mode %v", name, info.Mode())
		}
	}
}

func TestCorruptionBackupAndRepair(t *testing.T) {
	for _, corrupt := range []string{"not-json", "{}", "null", `{"schema":1}`, `{"schema":2,"revision":3,"updated_at":"2026-10-27T00:00:00Z","settings":{},"families":[],"aliases":[],"rules":[]}`} {
		t.Run(corrupt, func(t *testing.T) {
			dir := t.TempDir()
			s := openStore(t, dir)
			state := saveState(t, s, stateOf(t, s))
			state.Settings.WhitelistEnabled = true
			_ = saveState(t, s, state)
			if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(corrupt), 0600); err != nil {
				t.Fatal(err)
			}
			recovered := openStore(t, dir)
			if got := stateOf(t, recovered); got.Revision != 1 {
				t.Fatalf("expected backup revision 1: %+v", got)
			}
			if got := recovered.History(10, ""); len(got) != 1 || got[0].After.Revision != 1 {
				t.Fatal("history ahead of recovered backup survived")
			}
			// Saving after recovery must back up the healthy recovered state.
			_ = saveState(t, recovered, stateOf(t, recovered))
			data, err := os.ReadFile(filepath.Join(dir, "state.json.bak"))
			if err != nil {
				t.Fatal(err)
			}
			backup, err := decodeState(data)
			if err != nil || backup.Revision != 1 {
				t.Fatal("corrupt primary replaced healthy backup")
			}
			for _, name := range []string{"state.json", "state.json.bak"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(corrupt), 0600); err != nil {
					t.Fatal(err)
				}
			}
			broken := openStore(t, dir)
			deny, valid, err := broken.Load()
			if valid || !errors.Is(err, ErrUnavailable) || !deny.Settings.WhitelistEnabled {
				t.Fatalf("corruption failed open: valid=%v err=%v state=%+v", valid, err, deny)
			}
			fixed, err := broken.Save(deny.Revision, policy.DefaultState(), "user", "state", "repair")
			if err != nil || fixed.Revision != 1 {
				t.Fatalf("repair: %+v %v", fixed, err)
			}
			_ = stateOf(t, openStore(t, dir))
		})
	}
}

func TestOnlyBackupAndMissingPrimaryIsNotDefault(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	state := saveState(t, s, stateOf(t, s))
	_ = saveState(t, s, state)
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, openStore(t, dir)); got.Revision != 1 {
		t.Fatal("did not load backup")
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json.bak"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	_, valid, err := openStore(t, dir).Load()
	if valid || !errors.Is(err, ErrUnavailable) {
		t.Fatal("corrupt backup with missing primary became defaults")
	}
}

func TestValidJSONWithBrokenSchemaIsRejected(t *testing.T) {
	data, err := json.Marshal(policy.DefaultState())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		key   string
		value string
	}{
		{"missing whitelist field", "settings", `{"rewrite_response_model":false,"refresh_interval_seconds":300}`},
		{"null whitelist field", "settings", `{"whitelist_enabled":null,"rewrite_response_model":false,"refresh_interval_seconds":300}`},
		{"settings scalar", "settings", `false`},
		{"array null", "aliases", `null`},
		{"array entry null", "aliases", `[null]`},
		{"alias object incomplete", "aliases", `[{}]`},
		{"rule scalar", "rules", `[true]`},
		{"family object incomplete", "families", `[{"id":"f"}]`},
		{"future schema", "schema", `2`},
		{"negative revision", "revision", `-1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			fields[test.key] = json.RawMessage(test.value)
			corrupt, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeState(corrupt); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("accepted corrupt schema: %s", corrupt)
			}
		})
	}
}

func TestConcurrentRevisionWriters(t *testing.T) {
	s := openStore(t, t.TempDir())
	base := stateOf(t, s)
	var successes, conflicts atomic.Int32
	var wg sync.WaitGroup
	for range 24 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Save(0, base, "user", "state", "concurrent")
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, ErrRevisionConflict):
				conflicts.Add(1)
			default:
				t.Errorf("Save: %v", err)
			}
			_, _ = s.Current()
			_ = s.History(10, "")
		}()
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 23 || stateOf(t, s).Revision != 1 {
		t.Fatalf("success=%d conflicts=%d", successes.Load(), conflicts.Load())
	}
	if len(s.History(100, "")) != 1 {
		t.Fatal("conflicts generated history")
	}
}

func TestRestoreTargetsAndState(t *testing.T) {
	s := openStore(t, t.TempDir())
	state := stateOf(t, s)
	state.Aliases = []policy.Alias{{ID: "a", Name: "sol", Target: "old", Mode: "pinned"}, {ID: "b", Name: "other", Target: "kept", Mode: "pinned"}}
	state = saveState(t, s, state)
	state.Aliases[0].Target = "selected"
	state, err := s.Save(state.Revision, state, "auto", "alias_target", "new version")
	if err != nil {
		t.Fatal(err)
	}
	entry := s.History(1, "")[0]
	state.Aliases[0].Target = "newest"
	state.Aliases[1].Target = "other-new"
	state.Settings.WhitelistEnabled = true
	state = saveState(t, s, state)
	restored, err := s.Restore(state.Revision, entry.ID, "alias_target")
	if err != nil || restored.Revision != 4 || restored.Aliases[0].Target != "selected" || restored.Aliases[1].Target != "other-new" || !restored.Settings.WhitelistEnabled {
		t.Fatalf("target restore: %+v %v", restored, err)
	}
	full, err := s.Restore(restored.Revision, entry.ID, "state")
	if err != nil || full.Revision != 5 || full.Settings.WhitelistEnabled || full.Aliases[1].Target != "kept" {
		t.Fatalf("full restore: %+v %v", full, err)
	}
	if _, err := s.Restore(0, entry.ID, "state"); !errors.Is(err, ErrRevisionConflict) {
		t.Fatal(err)
	}
	if _, err := s.Restore(5, entry.ID, "bad"); !errors.Is(err, ErrInvalidScope) {
		t.Fatal(err)
	}
	if _, err := s.Restore(5, "missing", "state"); !errors.Is(err, ErrEntryNotFound) {
		t.Fatal(err)
	}
}

func TestHistoryCrashRecoveryAndCap(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	state := saveState(t, s, stateOf(t, s))
	entry := s.History(1, "")[0]
	orphan := entry
	orphan.ID = "2"
	orphan.Before = clone(state)
	orphan.After = clone(state)
	orphan.After.Revision = 2
	line, err := json.Marshal(orphan)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "history.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(append(append(line, '\n'), []byte("{partial")...)); err != nil {
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	if len(s.History(10, "")) != 1 {
		t.Fatal("uncommitted/truncated history exposed")
	}
	state = saveState(t, s, stateOf(t, s))
	if got := openStore(t, dir).History(10, ""); len(got) != 2 || got[0].After.Revision != 2 {
		t.Fatal("partial history poisoned next append")
	}
	// Seed a full valid journal to exercise real compaction with few fsyncs.
	entries := make([]Entry, HistoryLimit)
	for i := range entries {
		e := entry
		e.ID = fmt.Sprint(i + 1)
		e.Before = clone(state)
		e.Before.Revision = int64(i)
		e.After = clone(state)
		e.After.Revision = int64(i + 1)
		entries[i] = e
	}
	s.history = entries
	s.state.Revision = HistoryLimit
	if err := s.compactHistory(entries); err != nil {
		t.Fatal(err)
	}
	state = saveState(t, s, clone(s.state))
	got := openStore(t, dir).History(HistoryLimit+1, "")
	if len(got) != HistoryLimit || got[0].After.Revision != HistoryLimit+1 || got[len(got)-1].After.Revision != 2 {
		t.Fatalf("bad cap len=%d newest=%d oldest=%d", len(got), got[0].After.Revision, got[len(got)-1].After.Revision)
	}
}

func TestFailedPersistenceDoesNotPublishRevision(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	state := stateOf(t, s)
	// A directory at the destination makes rename fail after the WAL append.
	if err := os.Mkdir(filepath.Join(dir, "state.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(0, state, "user", "state", ""); err == nil {
		t.Fatal("expected write failure")
	}
	if got := stateOf(t, s); got.Revision != 0 || len(s.History(10, "")) != 0 {
		t.Fatal("failed save was published")
	}
	if err := os.Remove(filepath.Join(dir, "state.json")); err != nil {
		t.Fatal(err)
	}
	_ = saveState(t, s, state)
	if len(openStore(t, dir).History(10, "")) != 1 {
		t.Fatal("failed save left phantom history")
	}
}

func TestSemanticallyCorruptPrimaryFallsBackToLastGood(t *testing.T) {
	dir := t.TempDir()
	s := openStore(t, dir)
	first := saveState(t, s, stateOf(t, s))
	second := first
	second.Settings.WhitelistEnabled = true
	second = saveState(t, s, second)
	second.Rules = []policy.Rule{{ID: "bad", Enabled: true, Kind: "unknown", Value: "anything"}}
	data, _ := json.Marshal(second)
	if e := os.WriteFile(filepath.Join(dir, "state.json"), data, 0600); e != nil {
		t.Fatal(e)
	}
	restored := stateOf(t, openStore(t, dir))
	if restored.Revision != first.Revision || restored.Settings.WhitelistEnabled {
		t.Fatal(restored)
	}
}
