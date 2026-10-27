package mgmt

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"testing"

	"github.com/cpa-model-dock/cpa-model-dock/internal/autoupdate"
	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func keys(t *testing.T, value any, want ...string) map[string]any {
	t.Helper()
	obj, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object, got %T: %v", value, value)
	}
	actual := []string{}
	for key := range obj {
		actual = append(actual, key)
	}
	sort.Strings(actual)
	sort.Strings(want)
	if !reflect.DeepEqual(actual, want) {
		t.Fatalf("keys=%v want=%v object=%v", actual, want, obj)
	}
	return obj
}
func reasonShape(t *testing.T, value any) {
	t.Helper()
	obj := keys(t, value, "code", "message")
	for _, key := range []string{"code", "message"} {
		if text, ok := obj[key].(string); !ok || text == "" {
			t.Fatalf("invalid reason %v", obj)
		}
	}
}
func stateShape(t *testing.T, value any) map[string]any {
	t.Helper()
	obj := keys(t, value, "schema", "revision", "updated_at", "settings", "families", "builtin_families", "aliases", "rules", "alias_status")
	keys(t, obj["settings"], "whitelist_enabled", "rewrite_response_model", "refresh_interval_seconds")
	for _, key := range []string{"families", "builtin_families", "aliases", "rules"} {
		if _, ok := obj[key].([]any); !ok {
			t.Fatalf("%s must be an array: %v", key, obj)
		}
	}
	for _, status := range obj["alias_status"].(map[string]any) {
		row := keys(t, status, "current_target", "candidates", "last_checked_at", "last_result", "reason", "target_routable")
		reasonShape(t, row["reason"])
	}
	return obj
}

func TestEndpointResponseFieldShapes(t *testing.T) {
	f := setup(t)
	s := f.current.Load()
	state := s.StateCopy()
	state.Aliases = []policy.Alias{{ID: "al_sol", Name: "sol", Enabled: true, Mode: "auto", Target: "gpt-6.1-sol", Auto: policy.Auto{Family: "builtin:gpt-sol"}}}
	state.Rules = []policy.Rule{{ID: "r_family", Enabled: true, Kind: "family", Value: "builtin:gpt-sol"}}
	f.current.Store(policy.New(state, s.CatalogCopy(), true))
	var cached map[string]autoupdate.Status
	f.h.config.AliasStatus = func() any { return cached }
	f.h.config.Check = func(ctx context.Context, _ string, dry bool) (any, error) {
		_, status := autoupdate.Evaluate(ctx, f.current.Load(), nil)
		if !dry {
			cached = status
		}
		return status, nil
	}
	current := stateShape(t, decode(t, call(t, f.h, "GET", "/state", nil), 200))
	initialStatus := current["alias_status"].(map[string]any)["al_sol"].(map[string]any)
	if initialStatus["last_checked_at"] != nil {
		t.Fatal(initialStatus)
	}
	o := decode(t, call(t, f.h, "GET", "/overview", nil), 200)
	keys(t, o, "plugin_version", "host_schema", "policy_enabled", "whitelist_enabled", "counts", "active_mappings", "recent_auto_updates", "catalog", "guard", "warnings")
	keys(t, o["counts"], "public_models", "public_aliases")
	keys(t, o["catalog"], "last_refresh_at", "ok", "error")
	guard := keys(t, o["guard"], "enabled", "verified", "limitations")
	if guard["verified"] != false {
		t.Fatal(guard)
	}
	keys(t, o["active_mappings"].([]any)[0], "alias", "target")
	for _, path := range []string{"/catalog", "/catalog/refresh"} {
		method := "GET"
		var body any
		if path == "/catalog/refresh" {
			method = "POST"
			body = map[string]any{"revision": "ignored"}
		}
		c := decode(t, call(t, f.h, method, path, body), 200)
		keys(t, c, "refreshed_at", "ok", "error", "models")
		if c["error"] != nil {
			t.Fatal(c)
		}
		for _, entry := range c["models"].([]any) {
			row := keys(t, entry, "id", "providers", "credential_count", "routable", "public", "reason")
			reasonShape(t, row["reason"])
		}
	}
	check := decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"dry_run": true, "revision": "ignored"}), 200)
	keys(t, check, "results", "revision")
	result := keys(t, check["results"].([]any)[0], "alias_id", "current_target", "candidates", "last_checked_at", "last_result", "reason")
	reasonShape(t, result["reason"])
	if result["reason"].(map[string]any)["code"] != "newer_version" || result["current_target"] != "gpt-6.2-sol" {
		t.Fatal(result)
	}
	for _, candidate := range result["candidates"].([]any) {
		row := keys(t, candidate, "id", "version", "routable", "chosen", "reason")
		reasonShape(t, row["reason"])
	}
	if !reflect.DeepEqual(current, decode(t, call(t, f.h, "GET", "/state", nil), 200)) {
		t.Fatal("dry-run mutated state/status")
	}
	decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": 0}), 200)
	stateShape(t, decode(t, call(t, f.h, "GET", "/state", nil), 200))
	preview := decode(t, call(t, f.h, "POST", "/rules/preview", map[string]any{}), 200)
	keys(t, preview, "rules", "public_models", "public_aliases")
	rule := keys(t, preview["rules"].([]any)[0], "id", "matches")
	if !reflect.DeepEqual(rule["matches"], []any{"gpt-6.1-sol", "gpt-6.2-sol", "gpt-6.3-sol"}) {
		t.Fatal(rule)
	}
	for _, model := range []string{"sol", "gpt-6.1-sol"} {
		e := decode(t, call(t, f.h, "POST", "/explain", map[string]any{"model": model}), 200)
		want := []string{"allowed", "status", "code", "reason", "steps"}
		if model == "sol" {
			want = append(want, "resolved_target")
		}
		keys(t, e, want...)
		for _, step := range e["steps"].([]any) {
			reasonShape(t, step)
		}
	}
	v := decode(t, call(t, f.h, "POST", "/validate", current), 200)
	keys(t, v, "valid", "details", "preview")
	keys(t, v["preview"], "public_models", "public_aliases")
	stateShape(t, decode(t, call(t, f.h, "PUT", "/state", current), 200))
	stateShape(t, decode(t, call(t, f.h, "POST", "/history/restore", map[string]any{"revision": 1, "entry_id": "history1", "scope": "state"}), 200))
	conflict := decode(t, call(t, f.h, "PUT", "/state", current), 409)
	keys(t, conflict, "error", "state")
	keys(t, conflict["error"], "code", "message")
	stateShape(t, conflict["state"])
	invalid := decode(t, call(t, f.h, "POST", "/rules/preview", map[string]any{"aliases": []any{map[string]any{"name": "bad/name"}}}), 422)
	err := keys(t, invalid["error"], "code", "message", "details")
	for _, detail := range err["details"].([]any) {
		keys(t, detail, "path", "code", "message")
	}
}

func TestHistoryPaginationEnvelopeAndBoundaries(t *testing.T) {
	f := setup(t)
	f.h.config.History = func(int, string) any {
		return []any{map[string]any{"id": "three"}, map[string]any{"id": "two"}, map[string]any{"id": "one"}}
	}
	page := func(query url.Values, status int) map[string]any {
		return decode(t, f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: BasePath + "/history", Query: query}), status)
	}
	first := page(url.Values{"limit": {"2"}}, 200)
	keys(t, first, "entries", "next_before")
	if first["next_before"] != "two" {
		t.Fatal(first)
	}
	last := page(url.Values{"limit": {"2"}, "before": {"two"}}, 200)
	if len(last["entries"].([]any)) != 1 || last["next_before"] != nil {
		t.Fatal(last)
	}
	page(url.Values{"before": {"missing"}}, 400)
	page(url.Values{"before": {"one", "two"}}, 400)
	page(url.Values{"limit": {"1", "2"}}, 400)
}

func TestOfflineTargetsDefaultsAndEditableProjection(t *testing.T) {
	f := setup(t)
	body := map[string]any{"revision": 0, "schema": nil, "updated_at": nil, "builtin_families": nil, "settings": policy.DefaultState().Settings,
		"families": []any{map[string]any{"name": "Custom", "template": "custom-{version}"}},
		"aliases":  []any{map[string]any{"id": "stable", "name": "offline", "mode": "pinned", "target": "missing-model", "status": nil, "unknown": nil}},
		"rules":    []any{map[string]any{"kind": "exact", "value": "missing-model"}}}
	saved := stateShape(t, decode(t, call(t, f.h, "PUT", "/state", body), 200))
	alias := saved["aliases"].([]any)[0].(map[string]any)
	if alias["id"] != "stable" || alias["enabled"] != true || alias["expose_original"] != false || alias["rewrite_response_model"] != nil {
		t.Fatal(alias)
	}
	if saved["families"].([]any)[0].(map[string]any)["allow_dated"] != true {
		t.Fatal(saved)
	}
	e := decode(t, call(t, f.h, "POST", "/explain", map[string]any{"model": "offline"}), 200)
	if e["status"] != float64(503) || e["code"] != "alias_target_unavailable" {
		t.Fatal(e)
	}
	// Missing settings and wrong types retain actionable paths.
	body["revision"] = 1
	body["settings"] = map[string]any{"whitelist_enabled": false}
	bad := decode(t, call(t, f.h, "PUT", "/state", body), 422)
	if len(bad["error"].(map[string]any)["details"].([]any)) != 2 {
		t.Fatal(bad)
	}
	r := f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: BasePath + "/aliases/check", Headers: http.Header{"If-Match": {`"0"`}}, Body: []byte(`{"revision":1}`)})
	stateShape(t, decode(t, r, 409)["state"])
}
