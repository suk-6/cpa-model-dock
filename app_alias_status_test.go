package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/suk-6/cpa-model-dock/internal/autoupdate"
	"github.com/suk-6/cpa-model-dock/internal/mgmt"
	"github.com/suk-6/cpa-model-dock/internal/policy"
	"github.com/suk-6/cpa-model-dock/internal/store"
)

func aliasStatuses(a *application) map[string]autoupdate.Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := map[string]autoupdate.Status{}
	for id, status := range a.status {
		status.Candidates = append([]autoupdate.Candidate{}, status.Candidates...)
		out[id] = status
	}
	return out
}

func awaitAliasStatus(t *testing.T, a *application, id string, after time.Time) autoupdate.Status {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		if status := aliasStatuses(a)[id]; status.LastCheckedAt.After(after) {
			return status
		}
		select {
		case <-deadline:
			t.Fatal("alias was not evaluated", aliasStatuses(a))
		case <-tick.C:
		}
	}
}

func checkedStatusApplication(t *testing.T) (*application, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := newApplication(dir, fakeHost())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	a.refresh(context.Background())
	state := autoState()
	state.Aliases[0].Target = "gpt-6.2-sol"
	state.Aliases[0].Auto.Family = "fam_test"
	state.Families = []policy.Family{{ID: "fam_test", Name: "Test", Template: "gpt-{version}-sol", Provider: "codex"}}
	if _, err := seedApplication(a, state); err != nil {
		t.Fatal(err)
	}
	if _, err := a.check(context.Background(), "", false); err != nil {
		t.Fatal(err)
	}
	return a, dir
}

func TestAliasStatusPreservedAcrossUnrelatedSavesAndRestart(t *testing.T) {
	a, dir := checkedStatusApplication(t)
	want := aliasStatuses(a)
	state := a.current.Load().StateCopy()
	state.Settings.RewriteResponseModel = !state.Settings.RewriteResponseModel
	state.Settings.RefreshIntervalSeconds = 60
	state.Rules[0].Note = "Changed rule note"
	state.Aliases[0].ExposeOriginal = true
	rewrite := true
	state.Aliases[0].RewriteResponseModel = &rewrite
	state.Families[0].Name = "Renamed family"
	if _, err := a.save(state.Revision, state, "user", "state", "Unrelated edits"); err != nil {
		t.Fatal(err)
	}
	if got := aliasStatuses(a); !reflect.DeepEqual(got, want) {
		t.Fatal("unrelated edits invalidated status", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, aliasStatusFilename))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("cache must be private", info, err)
	}
	a.close()
	loaded, err := newApplication(dir, fakeHost())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.close()
	if got := aliasStatuses(loaded); !reflect.DeepEqual(got, want) {
		t.Fatal("completed checks lost on restart", got, want)
	}
}

func TestAliasStatusCacheRejectsChangedEvaluationInputs(t *testing.T) {
	a, dir := checkedStatusApplication(t)
	tests := []struct {
		name   string
		change func(*policy.State)
	}{
		{"id", func(s *policy.State) { s.Aliases[0].ID = "replacement" }},
		{"mode", func(s *policy.State) { s.Aliases[0].Mode = "pinned" }},
		{"target", func(s *policy.State) { s.Aliases[0].Target = "gpt-6.1-sol" }},
		{"provider", func(s *policy.State) { s.Aliases[0].Provider = "" }},
		{"enabled", func(s *policy.State) { s.Aliases[0].Enabled = false }},
		{"prerelease", func(s *policy.State) { s.Aliases[0].Auto.IncludePrerelease = true }},
		{"health", func(s *policy.State) { s.Aliases[0].Auto.HealthCheck = true }},
		{"family", func(s *policy.State) { s.Aliases[0].Auto.Family = "builtin:gpt-sol" }},
		{"family_template", func(s *policy.State) { s.Families[0].Template = "gpt-{version}-astra" }},
		{"family_provider", func(s *policy.State) { s.Families[0].Provider = "" }},
		{"family_dates", func(s *policy.State) { s.Families[0].AllowDated = true }},
		{"removed", func(s *policy.State) { s.Aliases = s.Aliases[1:]; s.Rules = s.Rules[1:] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := a.current.Load().StateCopy()
			tt.change(&state)
			cached := loadAliasStatus(filepath.Join(dir, aliasStatusFilename), policy.New(state, a.current.Load().CatalogCopy(), true))
			if _, ok := cached["al_one"]; ok {
				t.Fatal("stale status accepted", cached)
			}
			if _, ok := cached["al_two"]; !ok {
				t.Fatal("unrelated cached alias discarded", cached)
			}
		})
	}
	// Simulate policy commit followed by a crash before the cache was replaced.
	state := a.current.Load().StateCopy()
	state.Aliases[0].Auto.IncludePrerelease = true
	if _, err := a.store.Save(state.Revision, state, "user", "state", "Changed auto settings"); err != nil {
		t.Fatal(err)
	}
	a.close()
	loaded, err := newApplication(dir, fakeHost())
	if err != nil {
		t.Fatal(err)
	}
	defer loaded.close()
	if cached := aliasStatuses(loaded); len(cached) != 1 || cached["al_two"].LastCheckedAt.IsZero() {
		t.Fatal("restart accepted mismatched cache", cached)
	}
}

type statusProbeHost struct {
	calls atomic.Int32
	fail  bool
}

func (h *statusProbeHost) Call(ctx context.Context, method string, in, out any) error {
	if method == pluginabi.MethodHostModelExecute {
		h.calls.Add(1)
		if h.fail {
			return errors.New("secret-token private-credential private-path")
		}
	}
	return fakeHost().Call(ctx, method, in, out)
}

func TestUserSaveEvaluatesNewAliasAndChangedAutoSettings(t *testing.T) {
	for _, health := range []bool{false, true} {
		t.Run(map[bool]string{false: "without_probe", true: "with_probe"}[health], func(t *testing.T) {
			host := &statusProbeHost{}
			a, err := newApplication(t.TempDir(), host)
			if err != nil {
				t.Fatal(err)
			}
			defer a.close()
			a.refresh(context.Background())
			state := autoState()
			state.Aliases[0].Auto.HealthCheck = health
			if _, err := a.save(0, state, "user", "state", "Create auto alias"); err != nil {
				t.Fatal(err)
			}
			status := awaitAliasStatus(t, a, "al_one", time.Time{})
			if status.LastResult != "updated" || a.current.Load().StateCopy().Aliases[0].Target != "gpt-6.2-sol" {
				t.Fatal(status, a.current.Load().StateCopy())
			}
			wantCalls := int32(0)
			if health {
				wantCalls = 1
			}
			if host.calls.Load() != wantCalls {
				t.Fatal("health probe opt-in ignored", host.calls.Load())
			}
			state = a.current.Load().StateCopy()
			state.Aliases[0].Auto.IncludePrerelease = true
			if _, err := a.save(state.Revision, state, "user", "state", "Change auto settings"); err != nil {
				t.Fatal(err)
			}
			updated := awaitAliasStatus(t, a, "al_one", status.LastCheckedAt)
			if updated.Reason != "no_newer_candidate" || len(updated.Candidates) != 2 || host.calls.Load() != wantCalls {
				t.Fatal("settings change was not re-evaluated", updated, host.calls.Load())
			}
		})
	}
}

func TestSavedAutoSettingsProbeDoesNotBlockMutationLock(t *testing.T) {
	host := newBlockedProbeHost(t)
	a, err := newApplication(t.TempDir(), host)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.refresh(context.Background())
	if _, err := seedApplication(a, autoState()); err != nil {
		t.Fatal(err)
	}
	state := a.current.Load().StateCopy()
	state.Aliases[0].Auto.HealthCheck = true
	saved := make(chan error, 1)
	go func() { _, err := a.save(state.Revision, state, "user", "state", "Enable health check"); saved <- err }()
	if err := awaitLifecycle(t, saved); err != nil {
		t.Fatal(err)
	}
	awaitLifecycle(t, host.entered)
	// A second policy mutation must finish while the asynchronous probe is stuck.
	state = a.current.Load().StateCopy()
	state.Aliases[0].Mode = "pinned"
	go func() { _, err := a.save(state.Revision, state, "user", "state", "Pin alias"); saved <- err }()
	if err := awaitLifecycle(t, saved); err != nil {
		t.Fatal(err)
	}
	host.release <- struct{}{}
	a.stop()
	if len(aliasStatuses(a)) != 0 || a.current.Load().StateCopy().Aliases[0].Target != "gpt-6.1-sol" {
		t.Fatal("obsolete settings evaluation committed")
	}
}

func TestCatalogRefreshWithUnchangedTargetPersistsCompletedCheck(t *testing.T) {
	a, dir := checkedStatusApplication(t)
	before := aliasStatuses(a)["al_one"]
	revision := a.current.Load().StateCopy().Revision
	history := len(a.store.History(100, ""))
	a.onCatalog(a.catalog.Refresh(context.Background()))
	after := aliasStatuses(a)["al_one"]
	if !after.LastCheckedAt.After(before.LastCheckedAt) || after.Reason != "no_newer_candidate" || len(after.Candidates) != 2 || a.current.Load().StateCopy().Revision != revision || len(a.store.History(100, "")) != history {
		t.Fatal("unchanged refresh did not record completed check", before, after)
	}
	cached := loadAliasStatus(filepath.Join(dir, aliasStatusFilename), a.current.Load())
	if !reflect.DeepEqual(cached["al_one"], after) {
		t.Fatal("check status not persisted", cached)
	}
}

func TestFailedAutomaticProbePersistsOnlySanitizedStatus(t *testing.T) {
	dir := t.TempDir()
	host := &statusProbeHost{fail: true}
	a, err := newApplication(dir, host)
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.refresh(context.Background())
	state := autoState()
	state.Aliases[0].Auto.HealthCheck = true
	if _, err := a.save(0, state, "user", "state", "Create alias"); err != nil {
		t.Fatal(err)
	}
	status := awaitAliasStatus(t, a, "al_one", time.Time{})
	if status.Reason != "health_check_failed" {
		t.Fatal(status)
	}
	data, err := os.ReadFile(filepath.Join(dir, aliasStatusFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret-token", "private-credential", "private-path", "credential-1", "auth_index"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatal("private host data in status cache", string(data))
		}
	}
}

func TestManagementAliasRestorePinsTargetInOneRevision(t *testing.T) {
	a, err := newApplication(t.TempDir(), fakeHost())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.refresh(context.Background())
	if _, err := seedApplication(a, autoState()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.check(context.Background(), "al_one", false); err != nil {
		t.Fatal(err)
	}
	entry := a.store.History(1, "")[0]
	body, _ := json.Marshal(map[string]any{"revision": 2, "entry_id": entry.ID + ":al_one", "scope": "alias_target", "pin": true})
	out := a.management(7, true).Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: mgmt.BasePath + "/history/restore", Body: body})
	if out.StatusCode != 200 {
		t.Fatal(out.StatusCode, string(out.Body))
	}
	state := a.current.Load().StateCopy()
	if state.Revision != 3 || state.Aliases[0].Target != "gpt-6.1-sol" || state.Aliases[0].Mode != "pinned" || len(a.store.History(10, "")) != 3 {
		t.Fatal("restore and pin must commit together", state)
	}
	last := a.store.History(1, "")[0]
	if last.Before.Aliases[0].Target != "gpt-6.2-sol" || last.Before.Aliases[0].Mode != "auto" || last.After.Aliases[0].Mode != "pinned" {
		t.Fatal(last)
	}
	row := a.managementHistory(100, "").([]any)[0].(map[string]any)
	if row["before"].(map[string]any)["mode"] != "auto" || row["after"].(map[string]any)["mode"] != "pinned" {
		t.Fatal("history omitted restored mode change", row)
	}
	if _, err := a.restorePinned(2, entry.ID+":al_one", "alias_target", true); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatal(err)
	}
	a.onCatalog(a.catalog.Refresh(context.Background()))
	if a.current.Load().StateCopy().Aliases[0].Target != "gpt-6.1-sol" {
		t.Fatal("refresh changed pinned restored target")
	}
}

func TestManagementAliasRestoreHistoryIncludesSameTargetPin(t *testing.T) {
	a, err := newApplication(t.TempDir(), fakeHost())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.refresh(context.Background())
	if _, err := seedApplication(a, autoState()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.check(context.Background(), "al_one", false); err != nil {
		t.Fatal(err)
	}
	entryID := a.store.History(1, "")[0].ID + ":al_one"
	if _, err := a.restore(2, entryID, "alias_target"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.restorePinned(3, entryID, "alias_target", true); err != nil {
		t.Fatal(err)
	}
	row := a.managementHistory(100, "").([]any)[0].(map[string]any)
	before, after := row["before"].(map[string]any), row["after"].(map[string]any)
	if before["target"] != after["target"] || before["mode"] != "auto" || after["mode"] != "pinned" {
		t.Fatal("mode-only history must expose pin change", row)
	}
	// Mode-only rows still support target restoration. Restore-only deliberately
	// preserves the current mode: alias_target does not undo the pin operation.
	restored, err := a.restorePinned(4, row["id"].(string), "alias_target", false)
	if err != nil || restored.Revision != 5 || restored.Aliases[0].Mode != "pinned" || restored.Aliases[0].Target != before["target"] {
		t.Fatal("mode-only row must remain restorable without unpinning", restored, err)
	}
}
