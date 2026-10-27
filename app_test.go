package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/suk-6/cpa-model-dock/internal/autoupdate"
	"github.com/suk-6/cpa-model-dock/internal/policy"
	"github.com/suk-6/cpa-model-dock/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func autoState() policy.State {
	s := policy.DefaultState()
	s.Settings.WhitelistEnabled = true
	s.Aliases = []policy.Alias{{ID: "al_one", Name: "sol", Enabled: true, Mode: "auto", Target: "gpt-6.1-sol", Provider: "codex", Auto: policy.Auto{Family: "builtin:gpt-sol"}}, {ID: "al_two", Name: "other-sol", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol", Provider: "codex"}}
	s.Rules = []policy.Rule{{ID: "r_one", Enabled: true, Kind: "alias", Value: "sol"}, {ID: "r_two", Enabled: true, Kind: "alias", Value: "other-sol"}}
	return s
}
func TestAutomaticUpdateHistoryRestartAndRestore(t *testing.T) {
	dir := t.TempDir()
	a, e := newApplication(dir, fakeHost())
	if e != nil {
		t.Fatal(e)
	}
	defer a.close()
	a.refresh(context.Background())
	saved, e := a.save(0, autoState(), "user", "state", "Configure aliases")
	if e != nil {
		t.Fatal(e)
	}
	// Filtered checks retain all aliases/rules in the policy, and dry-run never saves.
	result, e := a.checkRevision(context.Background(), saved.Revision, "al_one", true)
	if e != nil {
		t.Fatal(e)
	}
	statuses := result.(map[string]autoupdate.Status)
	if statuses["al_one"].CurrentTarget != "gpt-6.2-sol" || a.current.Load().StateCopy().Revision != 1 {
		t.Fatal(statuses)
	}
	if _, e = a.checkRevision(context.Background(), 1, "al_one", false); e != nil {
		t.Fatal(e)
	}
	state := a.current.Load().StateCopy()
	if state.Revision != 2 || state.Aliases[0].Target != "gpt-6.2-sol" || state.Aliases[1].Target != "gpt-6.1-sol" {
		t.Fatal(state)
	}
	history := a.store.History(10, "")
	if len(history) != 2 || history[0].Actor != "auto" || history[0].Kind != "alias_target" {
		t.Fatal(history)
	}
	// Reload into another runtime only after the writer is stopped.
	a.close()
	loaded, e := newApplication(dir, fakeHost())
	if e != nil {
		t.Fatal(e)
	}
	defer loaded.close()
	if s := loaded.current.Load(); !s.Available() || s.Decide("sol").Code != "alias_target_unavailable" {
		t.Fatal(s.Decide("sol"))
	}
	loaded.refresh(context.Background())
	if len(loaded.store.History(10, "")) != 2 {
		t.Fatal("history lost on restart")
	}
	restored, e := loaded.restore(2, history[0].ID+":al_one", "alias_target")
	if e != nil || restored.Revision != 3 || restored.Aliases[0].Target != "gpt-6.1-sol" {
		t.Fatal(restored, e)
	}
	// Applying checks enforce policy concurrency; refresh ignores the policy token.
	if _, e = loaded.checkRevision(context.Background(), 2, "", false); !errors.Is(e, store.ErrRevisionConflict) {
		t.Fatal(e)
	}
	if _, e = loaded.refreshRevision(context.Background(), 2); e != nil {
		t.Fatal(e)
	}
	if _, e = loaded.checkRevision(context.Background(), 3, "al_two", false); e != nil {
		t.Fatal(e)
	}
}
func TestBothCorruptRemainsRepairableAndFailClosed(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"state.json", "state.json.bak"} {
		if e := os.WriteFile(filepath.Join(dir, name), []byte(`{"bad":true}`), 0600); e != nil {
			t.Fatal(e)
		}
	}
	a, e := newApplication(dir, fakeHost())
	if e != nil {
		t.Fatal(e)
	}
	defer a.close()
	a.refresh(context.Background())
	if a.current.Load().Available() {
		t.Fatal("corruption replaced with defaults")
	}
	denied := a.hooks.Before(pluginapi.RequestInterceptRequest{RequestID: "corrupt", RequestedModel: "gpt-6.1-sol", Model: "gpt-6.1-sol"})
	if !denied.Terminate || denied.StatusCode != 503 {
		t.Fatal(denied)
	}
	handler := a.management(7, true)
	raw, _ := json.Marshal(policy.DefaultState())
	out := handler.Handle(context.Background(), pluginapi.ManagementRequest{Method: "PUT", Path: "/v0/management/plugins/cpa-model-dock/state", Body: raw})
	if out.StatusCode != 200 || !a.current.Load().Available() {
		t.Fatal(out.StatusCode, string(out.Body))
	}
}
func TestConfigurationAndInvalidReconfigureKeepLastGood(t *testing.T) {
	for _, raw := range []string{"state-dir: relative", "catalog-refresh-interval: 2s", "catalog-refresh-interval: garbage", "state-dir: [broken]"} {
		if _, _, e := parseConfig([]byte(raw)); e == nil {
			t.Fatal(raw)
		}
	}
	r := newRuntime(fakeHost())
	defer r.Close()
	registerTest(t, r, 7, t.TempDir())
	before := r.app
	var env struct {
		OK bool `json:"ok"`
	}
	_ = json.Unmarshal(r.Dispatch("plugin.reconfigure", mustJSON(map[string]any{"schema_version": 7, "config_yaml": []byte("state-dir: relative")})), &env)
	if env.OK || r.app != before {
		t.Fatal("invalid reconfiguration replaced last good application")
	}
}

func TestStateDirectoryReconfigureRetainsAdmittedRequest(t *testing.T) {
	r := newRuntime(fakeHost())
	defer r.Close()
	registerTest(t, r, 7, t.TempDir())
	r.app.refresh(context.Background())
	state := autoState()
	state.Aliases[0].Mode = "pinned"
	if _, e := r.app.save(0, state, "user", "state", "initial policy"); e != nil {
		t.Fatal(e)
	}
	var admitted pluginapi.RequestInterceptResponse
	req := pluginapi.RequestInterceptRequest{RequestID: "across-reconfigure", RequestedModel: "sol", Model: "gpt-6.1-sol", SourceFormat: "openai"}
	rpc(t, r, "request.intercept_before", req, &admitted)
	if admitted.Terminate {
		t.Fatal(admitted)
	}
	originalHooks := r.app.hooks
	rpc(t, r, "plugin.reconfigure", map[string]any{"schema_version": 7, "config_yaml": []byte("state-dir: " + t.TempDir() + "\n")}, nil)
	if r.app.hooks != originalHooks {
		t.Fatal("lost request pins during reconfigure")
	}
	rpc(t, r, "request.intercept_after", req, &admitted)
	if admitted.Terminate {
		t.Fatal("admitted request lost its pinned policy", admitted)
	}
	rpc(t, r, "request.complete", pluginapi.RequestCompletion{RequestID: req.RequestID}, nil)
}
