package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/suk-6/cpa-model-dock/internal/autoupdate"
	"github.com/suk-6/cpa-model-dock/internal/policy"
	"github.com/suk-6/cpa-model-dock/internal/store"
)

const aliasStatusFilename = "alias-status.json"
const maxAliasStatusBytes = 16 << 20

type persistedAliasStatus struct {
	Schema  int                         `json:"schema"`
	Aliases map[string]aliasStatusEntry `json:"aliases"`
}
type aliasStatusEntry struct {
	Fingerprint string            `json:"fingerprint"`
	Status      autoupdate.Status `json:"status"`
}

// Only evaluation inputs participate: display, rewrite, whitelist and refresh
// settings do not invalidate a completed check. Family labels are cosmetic too.
func aliasEvaluationFingerprints(snap *policy.Snapshot) map[string]string {
	out := map[string]string{}
	for _, alias := range snap.StateCopy().Aliases {
		family, found := snap.Family(alias.Auto.Family)
		input := struct {
			Enabled                  bool
			Mode, Target, Provider   string
			Auto                     policy.Auto
			FamilyFound              bool
			Template, FamilyProvider string
			AllowDated               bool
		}{alias.Enabled, alias.Mode, alias.Target, alias.Provider, alias.Auto, found, family.Template, family.Provider, family.AllowDated}
		data, _ := json.Marshal(input)
		digest := sha256.Sum256(data)
		out[alias.ID] = hex.EncodeToString(digest[:])
	}
	return out
}

func loadAliasStatus(path string, snap *policy.Snapshot) map[string]autoupdate.Status {
	out := map[string]autoupdate.Status{}
	if !snap.Available() {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAliasStatusBytes+1))
	var saved persistedAliasStatus
	if err != nil || len(data) > maxAliasStatusBytes || json.Unmarshal(data, &saved) != nil || saved.Schema != 1 {
		return out
	}
	for id, fingerprint := range aliasEvaluationFingerprints(snap) {
		if entry, ok := saved.Aliases[id]; ok && entry.Fingerprint == fingerprint {
			out[id] = entry.Status
		}
	}
	return out
}

// Called under a.mu. This cache is advisory; a failed cache write must never
// report an already committed policy mutation as failed. No host responses,
// credentials, policy snapshots or arbitrary upstream errors enter this file.
func (a *application) persistAliasStatus() {
	saved := persistedAliasStatus{Schema: 1, Aliases: map[string]aliasStatusEntry{}}
	for id, fingerprint := range aliasEvaluationFingerprints(a.current.Load()) {
		if status, ok := a.status[id]; ok {
			saved.Aliases[id] = aliasStatusEntry{fingerprint, status}
		}
	}
	data, err := json.Marshal(saved)
	if err == nil && len(data) <= maxAliasStatusBytes {
		_ = writeAliasStatus(a.statusPath, data)
	}
}

func writeAliasStatus(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".alias-status-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = dir.Sync()
	if closeErr := dir.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Reconcile after a policy mutation, retaining unrelated completed checks.
// Return changed inputs for asynchronous evaluation after the lock is released.
func (a *application) reconcileAliasStatus(before, after *policy.Snapshot) map[string]string {
	old, next := aliasEvaluationFingerprints(before), aliasEvaluationFingerprints(after)
	changed := map[string]string{}
	for id := range a.status {
		if next[id] == "" || old[id] != next[id] {
			delete(a.status, id)
		}
	}
	for _, alias := range after.StateCopy().Aliases {
		if old[alias.ID] != next[alias.ID] && alias.Enabled && alias.Mode == "auto" {
			changed[alias.ID] = next[alias.ID]
		}
	}
	return changed
}

// Capture the lifecycle under a.mu; evaluate takes immutable inputs under that
// lock and performs probes outside it. A newer input invalidates queued work.
// An unrelated revision can be retried without losing the user's requested check.
func (a *application) queueAliasEvaluation(changed map[string]string) {
	if len(changed) == 0 {
		return
	}
	life := a.workCtx
	go func() {
		for id, fingerprint := range changed {
			for {
				a.mu.Lock()
				snap := a.current.Load()
				valid := life == a.workCtx && life.Err() == nil && aliasEvaluationFingerprints(snap)[id] == fingerprint
				revision := snap.StateCopy().Revision
				a.mu.Unlock()
				if !valid {
					break
				}
				_, err := a.evaluate(life, &revision, nil, id, false)
				if !errors.Is(err, store.ErrRevisionConflict) {
					break
				}
				// A refresh completed but has not delivered its generation yet.
				// Its callback will evaluate the new catalog; do not spin on it.
				if a.catalog.Snapshot().Generation != a.current.Load().CatalogCopy().Generation {
					break
				}
			}
		}
	}()
}
