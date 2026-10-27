package mgmt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/cpa-model-dock/cpa-model-dock/internal/hooks"
	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/cpa-model-dock/cpa-model-dock/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestManagementPublicationImmediatelyChangesHooks(t *testing.T) {
	f := setup(t)
	hook := hooks.New(f.current.Load)
	defer hook.Close()
	oldRequest := pluginapi.RequestInterceptRequest{RequestID: "old", RequestedModel: "gpt-6.2-sol", Model: "gpt-6.2-sol", SourceFormat: "openai"}
	if hook.Before(oldRequest).Terminate {
		t.Fatal("default policy denied the pre-save request")
	}
	state := f.current.Load().StateCopy()
	state.Settings.WhitelistEnabled = true
	state.Aliases = []policy.Alias{{Name: "gpt-sol", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol"}}
	state.Rules = []policy.Rule{{Enabled: true, Kind: "alias", Value: "gpt-sol"}}
	decode(t, call(t, f.h, "PUT", "/state", state), 200)
	blocked := oldRequest
	blocked.RequestID = "new"
	if result := hook.Before(blocked); !result.Terminate || result.StatusCode != 403 {
		t.Fatalf("fresh request did not use the saved policy: %+v", result)
	}
	if result := hook.After(oldRequest); result.Terminate {
		t.Fatalf("in-flight request lost its original snapshot: %+v", result)
	}
	route := hook.Route(pluginapi.ModelRouteRequest{RequestedModel: "gpt-sol", AvailableProviders: []string{"codex"}, SourceFormat: "openai"})
	if !route.Handled || route.Target != "codex" || route.TargetModel != "gpt-6.1-sol" {
		t.Fatalf("alias did not route immediately: %+v", route)
	}
	alias := pluginapi.RequestInterceptRequest{RequestID: "alias", RequestedModel: "gpt-sol", Model: route.TargetModel, SourceFormat: "openai"}
	if result := hook.Before(alias); result.Terminate {
		t.Fatalf("published alias was denied: %+v", result)
	}
	hidden := pluginapi.RequestInterceptRequest{RequestID: "hidden", RequestedModel: route.TargetModel, Model: route.TargetModel, SourceFormat: "openai"}
	if result := hook.Before(hidden); !result.Terminate || result.StatusCode != 404 {
		t.Fatalf("hidden original leaked through hooks: %+v", result)
	}
}

func TestRevisionCallbacksTakePrecedenceAndForwardExpected(t *testing.T) {
	f := setup(t)
	f.h.config.Check = func(context.Context, string, bool) (any, error) {
		t.Fatal("compatibility Check called with CheckRevision configured")
		return nil, nil
	}
	f.h.config.Refresh = func(context.Context) policy.Catalog {
		t.Fatal("compatibility Refresh called with RefreshRevision configured")
		return policy.Catalog{}
	}
	var checks, refreshes int
	f.h.config.CheckRevision = func(ctx context.Context, expected int64, id string, dryRun bool) (any, error) {
		if ctx == nil || expected != 0 || id != "" || dryRun != (checks == 1) {
			t.Fatalf("incorrect check arguments: %d %q %v", expected, id, dryRun)
		}
		checks++
		return map[string]any{}, nil
	}
	f.h.config.RefreshRevision = func(ctx context.Context, expected int64) (policy.Catalog, error) {
		if ctx == nil || expected != 0 {
			t.Fatalf("incorrect refresh revision: %d", expected)
		}
		refreshes++
		return f.current.Load().CatalogCopy(), nil
	}
	decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": 0}), 200)
	decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"dry_run": true}), 200)
	decode(t, call(t, f.h, "POST", "/catalog/refresh", map[string]any{"revision": 0}), 200)
	if checks != 2 || refreshes != 1 {
		t.Fatalf("callback counts: %d %d", checks, refreshes)
	}
}

func TestOwnerRevisionRaceReturnsLatestState(t *testing.T) {
	for _, path := range []string{"/aliases/check", "/catalog/refresh"} {
		t.Run(path, func(t *testing.T) {
			f := setup(t)
			// Simulate an automatic save between the handler's snapshot read and
			// the callback acquiring the application's publication lock.
			conflict := func(expected int64) error {
				if expected != 0 {
					t.Fatalf("expected=%d", expected)
				}
				s := f.current.Load()
				state := s.StateCopy()
				state.Revision = 1
				f.current.Store(policy.New(state, s.CatalogCopy(), true))
				return fmt.Errorf("atomic callback: %w", store.ErrRevisionConflict)
			}
			f.h.config.CheckRevision = func(_ context.Context, expected int64, _ string, _ bool) (any, error) {
				return nil, conflict(expected)
			}
			f.h.config.RefreshRevision = func(_ context.Context, expected int64) (policy.Catalog, error) {
				return policy.Catalog{}, conflict(expected)
			}
			out := decode(t, call(t, f.h, "POST", path, map[string]any{"revision": 0}), 409)
			if out["current"].(map[string]any)["revision"] != float64(1) || f.checks != 0 || f.refreshes != 0 {
				t.Fatalf("race did not return latest state: %v", out)
			}
		})
	}
}

type validationFailure struct{ details []policy.Detail }

func (e validationFailure) Error() string                      { return "private validation context" }
func (e validationFailure) ValidationDetails() []policy.Detail { return e.details }

func TestOwnerValidationAndStoreErrors(t *testing.T) {
	validation := validationFailure{[]policy.Detail{{Path: "aliases[0].name", Code: "alias_conflict", Message: "Alias conflicts with current catalog"}}}
	for _, operation := range []string{"save", "restore", "check", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			f := setup(t)
			err := fmt.Errorf("owner: %w", validation)
			f.h.config.Save = func(int64, policy.State, string, string, string) (policy.State, error) { return policy.State{}, err }
			f.h.config.Restore = func(int64, string, string) (policy.State, error) { return policy.State{}, err }
			f.h.config.CheckRevision = func(context.Context, int64, string, bool) (any, error) { return nil, err }
			f.h.config.RefreshRevision = func(context.Context, int64) (policy.Catalog, error) { return policy.Catalog{}, err }
			var r pluginapi.ManagementResponse
			switch operation {
			case "save":
				r = call(t, f.h, "PUT", "/state", f.current.Load().StateCopy())
			case "restore":
				r = call(t, f.h, "POST", "/history/restore", map[string]any{"revision": 0, "entry_id": "entry", "scope": "state"})
			case "check":
				r = call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": 0})
			case "refresh":
				r = call(t, f.h, "POST", "/catalog/refresh", map[string]any{"revision": 0})
			}
			out := decode(t, r, 422)
			d := out["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
			if d["path"] != "aliases[0].name" || d["code"] != "alias_conflict" || strings.Contains(string(r.Body), "private") {
				t.Fatalf("validation details lost or private error leaked: %s", r.Body)
			}
		})
	}
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrUnavailable, 503, "policy_unavailable"},
		{store.ErrEntryNotFound, 404, "entry_not_found"},
		{store.ErrInvalidScope, 422, "validation_failed"},
		{store.ErrInvalidState, 422, "validation_failed"},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			f := setup(t)
			f.h.config.Save = func(int64, policy.State, string, string, string) (policy.State, error) {
				return policy.State{}, fmt.Errorf("private: %w", tc.err)
			}
			out := decode(t, call(t, f.h, "PUT", "/state", f.current.Load().StateCopy()), tc.status)
			if out["error"].(map[string]any)["code"] != tc.code {
				t.Fatalf("error=%v", out)
			}
			if errors.Is(tc.err, store.ErrInvalidState) {
				d := out["error"].(map[string]any)["details"].([]any)[0].(map[string]any)
				if d["path"] != "state" || d["code"] != "invalid_state" {
					t.Fatalf("invalid state details=%v", d)
				}
			}
		})
	}
}

func TestAliasStatusAlwaysCoversCurrentAliasesOnly(t *testing.T) {
	f := setup(t)
	s := f.current.Load()
	state := s.StateCopy()
	state.Aliases = []policy.Alias{
		{ID: "al_cached", Name: "cached", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol"},
		{ID: "al_new", Name: "new", Enabled: true, Mode: "pinned", Target: "gpt-6.2-sol"},
	}
	f.current.Store(policy.New(state, s.CatalogCopy(), true))
	f.h.config.AliasStatus = func() any {
		return map[string]any{
			"al_removed": map[string]any{"current_target": "ghost"},
			"al_cached":  map[string]any{"current_target": "old", "target_routable": false, "reason": "pinned", "message": "Alias is pinned", "candidates": nil},
		}
	}
	out := decode(t, call(t, f.h, "GET", "/state", nil), 200)
	statuses := out["alias_status"].(map[string]any)
	if len(statuses) != 2 || statuses["al_removed"] != nil {
		t.Fatalf("stale alias leaked: %v", statuses)
	}
	cached := statuses["al_cached"].(map[string]any)
	if cached["current_target"] != "gpt-6.1-sol" || cached["target_routable"] != true || cached["reason"] != "pinned" || cached["candidates"] == nil {
		t.Fatalf("cached runtime status did not reflect live state: %v", cached)
	}
	for _, key := range []string{"current_target", "candidates", "last_checked_at", "last_result", "reason", "message", "target_routable"} {
		if statuses["al_new"].(map[string]any)[key] == nil {
			t.Fatalf("new alias is missing %s: %v", key, statuses)
		}
	}
	decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": 0, "alias_id": "unknown"}), 404)
	if f.checks != 0 {
		t.Fatal("unknown alias invoked evaluation")
	}
}

func TestAbsentCallbacksAndEmptyAliasStatus(t *testing.T) {
	h := New(Config{AliasStatus: func() any { panic("no aliases should need no runtime callback") }})
	out := decode(t, call(t, h, "GET", "/state", nil), 200)
	if len(out["alias_status"].(map[string]any)) != 0 {
		t.Fatal("empty policy returned phantom aliases")
	}
	decode(t, call(t, h, "POST", "/aliases/check", map[string]any{"dry_run": true}), 503)
	decode(t, call(t, h, "POST", "/catalog/refresh", map[string]any{"revision": 0}), 503)
	explanation := decode(t, call(t, h, "POST", "/explain", map[string]any{"model": "anything"}), 200)
	if explanation["code"] != "policy_unavailable" || explanation["allowed"] != false {
		t.Fatalf("missing snapshot failed open: %v", explanation)
	}
}

func TestHistoryWrapsSlicesAndPreservesLargeRevisions(t *testing.T) {
	f := setup(t)
	f.h.config.History = func(int, string) any {
		return []store.Entry{{ID: "9007199254740993", Actor: "auto", After: policy.State{Revision: 9007199254740993}}}
	}
	r := call(t, f.h, "GET", "/history", nil)
	out := decode(t, r, 200)
	if len(out["entries"].([]any)) != 1 || !strings.Contains(string(r.Body), `"revision":9007199254740993`) {
		t.Fatalf("history did not preserve callback data: %s", r.Body)
	}
	out = decode(t, call(t, f.h, "GET", "/overview", nil), 200)
	if len(out["recent_auto_updates"].([]any)) != 1 {
		t.Fatalf("overview lost slice history: %v", out)
	}
}

func TestNestedNullsAndPartialDraftsKeepSavedState(t *testing.T) {
	f := setup(t)
	for _, body := range []string{
		`{"settings":{"whitelist_enabled":null}}`,
		`{"rules":[{"enabled":null,"kind":"exact","value":"gpt-6.1-sol"}]}`,
		`{"aliases":[{"name":"latest","enabled":null,"mode":"pinned","target":"gpt-6.1-sol"}]}`,
	} {
		r := f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: BasePath + "/rules/preview", Body: []byte(body)})
		decode(t, r, 422)
	}
	draft := map[string]any{
		"settings": map[string]any{"whitelist_enabled": true},
		"aliases":  []any{map[string]any{"name": "latest", "enabled": true, "mode": "pinned", "target": "gpt-6.1-sol", "rewrite_response_model": nil}},
		"rules":    []any{map[string]any{"kind": "alias", "value": "latest", "enabled": true}},
	}
	preview := decode(t, call(t, f.h, "POST", "/rules/preview", draft), 200)
	if len(preview["public_aliases"].([]any)) != 1 || f.saves != 0 || f.current.Load().StateCopy().Settings.WhitelistEnabled {
		t.Fatalf("new draft entries failed: %v", preview)
	}
	decode(t, call(t, f.h, "POST", "/explain", map[string]any{"model": "latest", "draft": draft}), http.StatusOK)
}
