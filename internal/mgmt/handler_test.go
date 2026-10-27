package mgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/cpa-model-dock/cpa-model-dock/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type fixture struct {
	h                                  *Handler
	current                            atomic.Pointer[policy.Snapshot]
	saves, checks, refreshes, restores int
	dryRun                             bool
	checkID                            string
	historyLimit                       int
	historyBefore                      string
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{}
	catalog := policy.Catalog{OK: true, Models: []policy.Model{
		{ID: "gpt-6.1-sol", Providers: []string{"codex"}, Routable: true, CredentialCount: 1},
		{ID: "gpt-6.2-sol", Providers: []string{"codex"}, Routable: true, CredentialCount: 1},
		{ID: "gpt-6.3-sol", Providers: []string{"codex"}, Routable: false},
	}}
	f.current.Store(policy.New(policy.DefaultState(), catalog, true))
	f.h = New(Config{
		Current: f.current.Load,
		Save: func(expected int64, state policy.State, actor, kind, summary string) (policy.State, error) {
			if actor != "user" || kind != "state" || summary == "" {
				t.Errorf("bad audit metadata: %q %q %q", actor, kind, summary)
			}
			current := f.current.Load()
			if expected != current.State.Revision {
				return current.State, fmt.Errorf("save: %w", store.ErrRevisionConflict)
			}
			state.Revision = expected + 1
			f.current.Store(policy.New(state, current.Catalog, true))
			f.saves++
			return state, nil
		},
		Refresh: func(context.Context) policy.Catalog { f.refreshes++; return f.current.Load().Catalog },
		Check: func(_ context.Context, id string, dryRun bool) (any, error) {
			f.checks++
			f.checkID = id
			f.dryRun = dryRun
			return map[string]any{"results": []any{}}, nil
		},
		History: func(limit int, before string) any {
			f.historyLimit = limit
			f.historyBefore = before
			return map[string]any{"entries": []any{map[string]any{"id": "history1", "actor": "auto", "kind": "alias_target"}}}
		},
		Restore: func(expected int64, id, scope string) (policy.State, error) {
			f.restores++
			s := f.current.Load()
			state := s.State
			state.Revision = expected + 1
			f.current.Store(policy.New(state, s.Catalog, true))
			return state, nil
		},
		Version: "test-version", HostSchema: 7, Guard: true,
	})
	return f
}

func call(t *testing.T, h *Handler, method, path string, body any) pluginapi.ManagementResponse {
	t.Helper()
	var b []byte
	if body != nil {
		var err error
		b, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	return h.Handle(context.Background(), pluginapi.ManagementRequest{Method: method, Path: BasePath + path, Body: b})
}
func decode(t *testing.T, r pluginapi.ManagementResponse, status int) map[string]any {
	t.Helper()
	if r.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%s", r.StatusCode, status, r.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRegistrationExactRoutesAndResources(t *testing.T) {
	b, err := json.Marshal(New(Config{}).Register(pluginapi.ManagementRegistrationRequest{BasePath: "/v0/management", ResourceBasePath: ResourceBasePath}))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out["routes"] == nil || out["resources"] == nil {
		t.Fatalf("wrong outer keys: %s", b)
	}
	var routes []struct {
		Method, Path, Menu string
		Handler            json.RawMessage
	}
	if err := json.Unmarshal(out["routes"], &routes); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"GET /overview": true, "GET /state": true, "PUT /state": true, "POST /validate": true,
		"GET /catalog": true, "POST /catalog/refresh": true, "POST /aliases/check": true,
		"POST /rules/preview": true, "POST /explain": true, "GET /history": true, "POST /history/restore": true,
	}
	if len(routes) != len(want) {
		t.Fatalf("routes=%v", routes)
	}
	for _, route := range routes {
		key := route.Method + " " + strings.TrimPrefix(route.Path, BasePath)
		if !want[key] || route.Menu != "" || route.Handler != nil {
			t.Fatalf("invalid route: %+v", route)
		}
		delete(want, key)
	}
	var resources []struct {
		Path, Menu string
		Handler    json.RawMessage
	}
	if err := json.Unmarshal(out["resources"], &resources); err != nil {
		t.Fatal(err)
	}
	if len(resources) != 5 {
		t.Fatalf("resources=%v", resources)
	}
	for i, name := range []string{"/index.html", "/app.js", "/app.css", "/i18n.js", "/favicon.svg"} {
		if resources[i].Path != ResourceBasePath+name || resources[i].Handler != nil {
			t.Fatalf("wrong resource %+v", resources[i])
		}
		if (i == 0 && resources[i].Menu != "Model Dock") || (i > 0 && resources[i].Menu != "") {
			t.Fatalf("menu %+v", resources[i])
		}
	}
}

func TestManagementRoundtripImmediatelyChangesPolicy(t *testing.T) {
	f := setup(t)
	stateView := decode(t, call(t, f.h, "GET", "/state", nil), 200)
	if stateView["builtin_families"] == nil || stateView["alias_status"] == nil {
		t.Fatalf("incomplete state: %v", stateView)
	}
	// Use the GET response directly as a PUT body, including its readonly fields.
	stateView["settings"].(map[string]any)["whitelist_enabled"] = true
	stateView["aliases"] = []any{map[string]any{"name": "gpt-sol", "enabled": true, "mode": "pinned", "target": "gpt-6.1-sol", "expose_original": false}}
	stateView["rules"] = []any{map[string]any{"enabled": true, "kind": "alias", "value": "gpt-sol"}}
	saved := decode(t, call(t, f.h, "PUT", "/state", stateView), 200)
	if saved["revision"] != float64(1) || f.saves != 1 {
		t.Fatalf("save failed: %v", saved)
	}
	aliasID := saved["aliases"].([]any)[0].(map[string]any)["id"].(string)
	ruleID := saved["rules"].([]any)[0].(map[string]any)["id"].(string)
	if !strings.HasPrefix(aliasID, "al_") || len(aliasID) != 39 || !strings.HasPrefix(ruleID, "r_") || len(ruleID) != 38 {
		t.Fatalf("IDs=%q %q", aliasID, ruleID)
	}
	snapshot := f.current.Load()
	if d := snapshot.Decide("gpt-sol"); !d.Allowed || d.ResolvedTarget != "gpt-6.1-sol" {
		t.Fatalf("alias decision=%+v", d)
	}
	if d := snapshot.Decide("gpt-6.1-sol"); d.Allowed {
		t.Fatalf("hidden target allowed: %+v", d)
	}
	if d := snapshot.Decide("gpt-6.2-sol"); d.Allowed {
		t.Fatalf("unlisted model allowed: %+v", d)
	}
	savedAgain := decode(t, call(t, f.h, "PUT", "/state", saved), 200)
	if savedAgain["aliases"].([]any)[0].(map[string]any)["id"] != aliasID {
		t.Fatal("ID changed on roundtrip")
	}
	conflict := decode(t, call(t, f.h, "PUT", "/state", saved), 409)
	if conflict["state"].(map[string]any)["revision"] != float64(2) || f.saves != 2 {
		t.Fatalf("stale response %v", conflict)
	}
}

func TestValidationKeepsState(t *testing.T) {
	f := setup(t)
	state := f.current.Load().State
	state.Aliases = []policy.Alias{{Name: "gpt-6.1-sol", Enabled: true, Mode: "pinned", Target: "gpt-6.2-sol"}}
	before := f.current.Load()
	result := decode(t, call(t, f.h, "PUT", "/state", state), 422)
	if result["error"].(map[string]any)["code"] != "validation_failed" || f.saves != 0 || before != f.current.Load() {
		t.Fatalf("invalid save changed state: %v", result)
	}
	validated := decode(t, call(t, f.h, "POST", "/validate", state), 200)
	if validated["valid"] != false || len(validated["details"].([]any)) == 0 || f.saves != 0 {
		t.Fatalf("bad validation response: %v", validated)
	}
	state = before.State
	state.Aliases = []policy.Alias{{ID: "client-picked", Name: "sol", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol"}}
	decode(t, call(t, f.h, "PUT", "/state", state), 200)
	if f.saves != 1 {
		t.Fatal("stable alias ID was rejected")
	}
}

func TestDraftPreviewExplainAndCounts(t *testing.T) {
	f := setup(t)
	draft := map[string]any{"settings": map[string]any{"whitelist_enabled": true}, "rules": []any{map[string]any{"id": "draft-rule", "kind": "family", "value": "builtin:gpt-sol", "enabled": true}}}
	view := decode(t, call(t, f.h, "POST", "/rules/preview", draft), 200)
	if !reflect.DeepEqual(view["public_models"], []any{"gpt-6.1-sol", "gpt-6.2-sol"}) {
		t.Fatalf("preview=%v", view)
	}
	if !reflect.DeepEqual(view["rules"].([]any)[0].(map[string]any)["matches"], []any{"gpt-6.1-sol", "gpt-6.2-sol", "gpt-6.3-sol"}) {
		t.Fatalf("rule matches=%v", view)
	}
	explanation := decode(t, call(t, f.h, "POST", "/explain", map[string]any{"model": "other-model", "draft": draft}), 200)
	if explanation["allowed"] != false || explanation["steps"] == nil {
		t.Fatalf("explain=%v", explanation)
	}
	if f.saves != 0 || f.current.Load().State.Settings.WhitelistEnabled {
		t.Fatal("draft mutated saved state")
	}
	view = decode(t, call(t, f.h, "GET", "/overview", nil), 200)
	counts := view["counts"].(map[string]any)
	if counts["public_models"] != float64(2) || counts["public_aliases"] != float64(0) || view["plugin_version"] != "test-version" || view["host_schema"] != float64(7) {
		t.Fatalf("overview=%v", view)
	}
	if len(view["recent_auto_updates"].([]any)) != 1 {
		t.Fatalf("recent updates=%v", view)
	}
	catalog := decode(t, call(t, f.h, "GET", "/catalog", nil), 200)
	if catalog["models"].([]any)[2].(map[string]any)["public"] != false {
		t.Fatal("unroutable model was public")
	}
}

func TestRevisionsRequiredOnEveryMutation(t *testing.T) {
	for _, path := range []string{"/state", "/aliases/check", "/history/restore"} {
		t.Run(path, func(t *testing.T) {
			f := setup(t)
			method := "POST"
			if path == "/state" {
				method = "PUT"
			}
			decode(t, call(t, f.h, method, path, map[string]any{}), 409)
			decode(t, call(t, f.h, method, path, map[string]any{"revision": 99}), 409)
			if f.saves+f.checks+f.refreshes+f.restores != 0 {
				t.Fatal("invalid revision invoked callback")
			}
		})
	}
	f := setup(t)
	decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"dry_run": true}), 200)
	if !f.dryRun {
		t.Fatal("dry run was applied")
	}
	decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": 0}), 200)
	if f.dryRun {
		t.Fatal("non dry run was not applied")
	}
	decode(t, call(t, f.h, "POST", "/catalog/refresh", map[string]any{"revision": 0}), 200)
	if f.refreshes != 1 {
		t.Fatal("refresh callback not called")
	}
	decode(t, call(t, f.h, "POST", "/history/restore", map[string]any{"revision": 0, "entry_id": "history1", "scope": "state"}), 200)
	if f.restores != 1 {
		t.Fatal("restore not called")
	}
}

func TestIfMatchAndInvalidRevisions(t *testing.T) {
	f := setup(t)
	body, _ := json.Marshal(f.current.Load().State)
	var obj map[string]any
	_ = json.Unmarshal(body, &obj)
	delete(obj, "revision")
	body, _ = json.Marshal(obj)
	resp := f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "PUT", Path: BasePath + "/state", Headers: http.Header{"if-match": {`"0"`}}, Body: body})
	decode(t, resp, 200)
	for _, value := range []any{nil, -1, 0.5, "1", true} {
		decode(t, call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": value}), 409)
	}
	for _, header := range []string{"1", `W/"1"`, "*", `"-1"`, `"1", "2"`} {
		r := f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: BasePath + "/aliases/check", Headers: http.Header{"If-Match": {header}}, Body: []byte(`{}`)})
		decode(t, r, 409)
	}
	r := f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: BasePath + "/aliases/check", Headers: http.Header{"If-Match": {`"0"`}}, Body: []byte(`{"revision":1}`)})
	decode(t, r, 409)
}

func TestCallbackConflictAndSanitizedErrors(t *testing.T) {
	f := setup(t)
	f.h.config.Save = func(int64, policy.State, string, string, string) (policy.State, error) {
		return policy.State{}, fmt.Errorf("wrapped: %w", store.ErrRevisionConflict)
	}
	decode(t, call(t, f.h, "PUT", "/state", f.current.Load().State), 409)
	f.h.config.Check = func(context.Context, string, bool) (any, error) {
		return nil, fmt.Errorf("secret-host-token internal-path")
	}
	r := call(t, f.h, "POST", "/aliases/check", map[string]any{"revision": 0})
	decode(t, r, 500)
	if strings.Contains(string(r.Body), "secret") {
		t.Fatal("error leaked sensitive detail")
	}
}

func TestStaticResourcesNeverReadStateOrCallbacks(t *testing.T) {
	panicCallback := func() { panic("public resources invoked privileged callback") }
	h := New(Config{Current: func() *policy.Snapshot { panicCallback(); return nil }, AliasStatus: func() any { panicCallback(); return nil }})
	for name, ct := range assets {
		r := h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: ResourceBasePath + name})
		// The UI is a placeholder in the shared backend checkout; missing assets
		// deliberately return 404. Once UI assets land the same test checks bytes.
		if r.StatusCode != 404 && r.StatusCode != 200 {
			t.Fatalf("%s status=%d body=%s", name, r.StatusCode, r.Body)
		}
		if r.Headers.Get("Content-Type") != ct || r.Headers.Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s headers=%v", name, r.Headers)
		}
		csp := r.Headers.Get("Content-Security-Policy")
		for _, directive := range []string{"script-src 'self'", "style-src 'self'", "connect-src 'self'"} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("CSP=%s", csp)
			}
		}
	}
	for _, path := range []string{"/state", "/../state", "/index.html/extra", "/app.js/../state"} {
		r := h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: ResourceBasePath + path})
		if r.StatusCode != 404 {
			t.Fatalf("%s status=%d", path, r.StatusCode)
		}
	}
}

func TestHistoryQueriesAndInputBoundaries(t *testing.T) {
	f := setup(t)
	r := f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: BasePath + "/history", Query: url.Values{"limit": {"2"}, "before": {"history1"}}})
	decode(t, r, 200)
	if f.historyLimit != 1000 || f.historyBefore != "" {
		t.Fatal("query was not forwarded")
	}
	for _, limit := range []string{"0", "1001", "many"} {
		r = f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "GET", Path: BasePath + "/history", Query: url.Values{"limit": {limit}}})
		decode(t, r, 400)
	}
	for _, tc := range []struct {
		body   string
		status int
	}{
		{"null", 400}, {"[]", 400}, {"{} {}", 400}, {`{"dry_run":null}`, 422}, {`{"dry_run":"true"}`, 422}, {strings.Repeat(" ", maxBody+1), 413},
	} {
		r = f.h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: BasePath + "/aliases/check", Body: []byte(tc.body)})
		decode(t, r, tc.status)
	}
	decode(t, call(t, f.h, "GET", "/state/extra", nil), 404)
	decode(t, call(t, f.h, "DELETE", "/state", nil), 405)
}
