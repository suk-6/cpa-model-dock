package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func fixture(target string, rewrite bool) *policy.Snapshot {
	state := policy.DefaultState()
	state.Settings.WhitelistEnabled = true
	state.Settings.RewriteResponseModel = rewrite
	state.Aliases = []policy.Alias{{ID: "a1", Name: "friendly", Enabled: true, Mode: "pinned", Target: target, Provider: "codex"}}
	state.Rules = []policy.Rule{{ID: "r1", Enabled: true, Kind: "alias", Value: "friendly"}, {ID: "r2", Enabled: true, Kind: "exact", Value: "native"}}
	return policy.New(state, policy.Catalog{OK: true, Prefixes: []string{"work"}, Models: []policy.Model{
		{ID: "gpt-6.1-sol", Routable: true, Providers: []string{"codex"}},
		{ID: "gpt-6.2-sol", Routable: true, Providers: []string{"codex"}},
		{ID: "gpt-6.3-sol", Routable: true, Providers: []string{"codex"}},
		{ID: "native", Routable: true, Providers: []string{"codex"}},
		{ID: "offline", Routable: false, Providers: []string{"codex"}},
	}}, true)
}

func hooksFor(t *testing.T, s *policy.Snapshot) *Hooks {
	t.Helper()
	if !s.Available() {
		t.Fatal("invalid test policy")
	}
	h := New(func() *policy.Snapshot { return s })
	t.Cleanup(h.Close)
	return h
}

func TestAdmissionAndResolvedChecks(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	tests := []struct {
		name, requested, model string
		before, after          int
	}{
		{"alias permits hidden target internally", "friendly", "gpt-6.1-sol", 0, 0},
		{"known prefix and thinking", "work/friendly(high)", "gpt-6.1-sol(high)", 0, 0},
		{"hidden direct target", "gpt-6.1-sol", "gpt-6.1-sol", 404, 404},
		{"public native request", "native", "native", 0, 0},
		{"unknown prefix", "invented/friendly", "gpt-6.1-sol", 403, 403},
		{"unknown model", "other", "other", 403, 403},
		{"alias changed upstream", "friendly", "gpt-6.2-sol", 503, 403},
		{"native maps to hidden model", "native", "gpt-6.1-sol", 0, 404},
		{"native maps to unpublished destination", "native", "other", 0, 403},
		{"resolved name is not a public alias", "native", "friendly", 0, 403},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := h.Before(pluginapi.RequestInterceptRequest{RequestID: tc.name + "-b", RequestedModel: tc.requested, Model: tc.model, SourceFormat: "openai"})
			after := h.After(pluginapi.RequestInterceptRequest{RequestID: tc.name + "-a", RequestedModel: tc.requested, Model: tc.model, SourceFormat: "openai", ToFormat: "openai", Metadata: map[string]any{"auth_id": "fake", "model_mapping": map[string]string{"gpt-6.1-sol": tc.model}}})
			assertTermination(t, before, tc.before)
			assertTermination(t, after, tc.after)
		})
	}
}

func assertTermination(t *testing.T, out pluginapi.RequestInterceptResponse, status int) {
	t.Helper()
	if out.Terminate != (status != 0) || out.StatusCode != status {
		t.Fatalf("got terminate=%v status=%d, want %d: %s", out.Terminate, out.StatusCode, status, out.ResponseBody)
	}
	if status != 0 && (!json.Valid(out.ResponseBody) || out.ResponseHeaders.Get("Content-Type") != "application/json") {
		t.Fatalf("denial is not a successful protocol response: %+v", out)
	}
}

func TestProtocolDenialsAreSafe(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	for _, format := range []string{"openai", "openai-response", "claude", "gemini"} {
		out := h.Before(pluginapi.RequestInterceptRequest{RequestID: format, RequestedModel: "gpt-6.1-sol", Model: "gpt-6.1-sol", SourceFormat: format})
		assertTermination(t, out, 404)
		if bytes.Contains(out.ResponseBody, []byte("gpt-6.1-sol")) {
			t.Fatal("hidden model leaked in denial")
		}
		var body map[string]any
		_ = json.Unmarshal(out.ResponseBody, &body)
		err := body["error"].(map[string]any)
		switch format {
		case "claude":
			if body["type"] != "error" || err["type"] != "not_found_error" {
				t.Fatal(string(out.ResponseBody))
			}
		case "gemini":
			if err["code"] != float64(404) || err["status"] != "NOT_FOUND" {
				t.Fatal(string(out.ResponseBody))
			}
		default:
			if err["code"] != "model_not_found" {
				t.Fatal(string(out.ResponseBody))
			}
		}
	}
}

func TestRouteAliasAndAlphaSentinel(t *testing.T) {
	s := fixture("gpt-6.1-sol", true)
	h := hooksFor(t, s)
	for _, format := range []string{"openai", "codex-alpha-search"} {
		out := h.Route(pluginapi.ModelRouteRequest{RequestedModel: "work/friendly(high)", SourceFormat: format, AvailableProviders: []string{"codex"}})
		if !out.Handled || out.TargetKind != pluginapi.ModelRouteTargetProvider || out.Target != "codex" || out.TargetModel != "gpt-6.1-sol(high)" {
			t.Fatalf("alias route: %+v", out)
		}
	}
	denied := h.Route(pluginapi.ModelRouteRequest{RequestedModel: "gpt-6.1-sol", SourceFormat: "codex-alpha-search", AvailableProviders: []string{"codex"}})
	if !denied.Handled || denied.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatalf("Alpha sentinel must survive a codex-only host: %+v", denied)
	}
	if h.Route(pluginapi.ModelRouteRequest{RequestedModel: "other", SourceFormat: "openai"}).Handled {
		t.Fatal("ordinary denial belongs to BeforeAuth")
	}
	if h.Route(pluginapi.ModelRouteRequest{RequestedModel: "friendly", AvailableProviders: []string{"claude"}}).Handled {
		t.Fatal("must not guess an alternate provider")
	}
	h.Guard = false
	if h.Route(pluginapi.ModelRouteRequest{RequestedModel: "other", SourceFormat: "codex-alpha-search"}).Handled {
		t.Fatal("disabled guard should not sentinel Alpha")
	}
	state := s.StateCopy()
	state.Aliases[0].Provider = ""
	catalog := s.CatalogCopy()
	catalog.Models[0].Providers = []string{"claude", "codex"}
	unpinned := hooksFor(t, policy.New(state, catalog, true))
	if out := unpinned.Route(pluginapi.ModelRouteRequest{RequestedModel: "friendly", AvailableProviders: []string{"codex"}}); out.Target != "codex" {
		t.Fatalf("available catalog provider was not selected: %+v", out)
	}
}

func TestRequestSnapshotPinAndRouterPublicationRace(t *testing.T) {
	var current atomic.Pointer[policy.Snapshot]
	first, second, third := fixture("gpt-6.1-sol", true), fixture("gpt-6.2-sol", false), fixture("gpt-6.3-sol", false)
	current.Store(first)
	h := New(current.Load)
	t.Cleanup(h.Close)
	route := h.Route(pluginapi.ModelRouteRequest{RequestedModel: "friendly", AvailableProviders: []string{"codex"}})
	h.Update(second)
	current.Store(second)
	request := pluginapi.RequestInterceptRequest{RequestID: "race", RequestedModel: "friendly", Model: route.TargetModel}
	assertTermination(t, h.Before(request), 0)
	h.Update(third)
	current.Store(third)
	assertTermination(t, h.After(request), 0)
	request.Model = "gpt-6.3-sol"
	assertTermination(t, h.After(request), 403)
	out := h.Response(pluginapi.ResponseInterceptRequest{RequestID: "race", RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "openai", Body: []byte(`{"model":"gpt-6.1-sol"}`)})
	if !bytes.Contains(out.Body, []byte(`"friendly"`)) {
		t.Fatalf("response did not use pinned rewrite setting: %s", out.Body)
	}
	// Only the immediate previous generation is eligible for a new request.
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "too-old", RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 503)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "previous", RequestedModel: "friendly", Model: "gpt-6.2-sol"}), 0)
	h.Complete(pluginapi.RequestCompletion{RequestID: "race", Outcome: pluginapi.RequestCompletionSucceeded})
	h.mu.Lock()
	_, exists := h.pins["race"]
	h.mu.Unlock()
	if exists {
		t.Fatal("completion retained request pin")
	}
	assertTermination(t, h.After(pluginapi.RequestInterceptRequest{RequestID: "race", RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 403)
}

func TestAliasRequestRewritesOnlyModel(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	body := []byte(`{"model":"friendly","messages":[{"content":"friendly","model":"do not touch"}],"seed":9007199254740993}`)
	original := bytes.Clone(body)
	out := h.Before(pluginapi.RequestInterceptRequest{RequestID: "body", RequestedModel: "friendly(high)", Model: "gpt-6.1-sol(high)", Body: body})
	assertTermination(t, out, 0)
	if !bytes.Contains(out.Body, []byte(`"model":"gpt-6.1-sol(high)"`)) || !bytes.Contains(out.Body, []byte(`"content":"friendly"`)) || !bytes.Contains(out.Body, []byte(`9007199254740993`)) {
		t.Fatal(string(out.Body))
	}
	if !bytes.Equal(body, original) {
		t.Fatal("request body mutated")
	}
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "malformed", RequestedModel: "friendly", Model: "gpt-6.1-sol", Body: []byte("{")}), 503)
}

func TestResponseModelFields(t *testing.T) {
	tests := []struct {
		format, body string
		contains     []string
	}{
		{"openai", `{"model":"gpt-6.1-sol","choices":[{"message":{"content":"gpt-6.1-sol","model":"literal"}}],"id":9007199254740993}`, []string{`"model":"friendly"`, `"content":"gpt-6.1-sol"`, `"model":"literal"`, `9007199254740993`}},
		{"openai-response", `{"type":"response.completed","response":{"model":"gpt-6.1-sol","output":[{"text":"gpt-6.1-sol"}]}}`, []string{`"model":"friendly"`, `"text":"gpt-6.1-sol"`}},
		{"claude", `{"type":"message_start","message":{"model":"gpt-6.1-sol","content":[]}}`, []string{`"model":"friendly"`}},
		{"gemini", `{"modelVersion":"gpt-6.1-sol","candidates":[{"content":{"parts":[{"text":"gpt-6.1-sol"}]}}]}`, []string{`"modelVersion":"friendly"`, `"text":"gpt-6.1-sol"`}},
	}
	for _, tc := range tests {
		t.Run(tc.format, func(t *testing.T) {
			h := hooksFor(t, fixture("gpt-6.1-sol", true))
			h.Before(pluginapi.RequestInterceptRequest{RequestID: tc.format, RequestedModel: "friendly", Model: "gpt-6.1-sol"})
			body := []byte(tc.body)
			out := h.Response(pluginapi.ResponseInterceptRequest{RequestID: tc.format, RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: tc.format, Body: body})
			for _, wanted := range tc.contains {
				if !bytes.Contains(out.Body, []byte(wanted)) {
					t.Fatalf("missing %s: %s", wanted, out.Body)
				}
			}
			if string(body) != tc.body {
				t.Fatal("read-only response mutated")
			}
			if !containsFold(out.ClearHeaders, "Content-Length") {
				t.Fatal("stale entity size retained")
			}
		})
	}
}

func TestAliasRewriteOverride(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s := fixture("gpt-6.1-sol", !enabled)
		state := s.StateCopy()
		state.Aliases[0].RewriteResponseModel = &enabled
		h := hooksFor(t, policy.New(state, s.CatalogCopy(), true))
		out := h.Response(pluginapi.ResponseInterceptRequest{RequestID: fmt.Sprint(enabled), RequestedModel: "friendly", Model: "gpt-6.1-sol", SourceFormat: "openai", Body: []byte(`{"model":"gpt-6.1-sol"}`)})
		if enabled != bytes.Contains(out.Body, []byte(`"friendly"`)) {
			t.Fatalf("override %v ignored: %s", enabled, out.Body)
		}
	}
}

func TestCatalogShapesCloneTargetAndFilter(t *testing.T) {
	tests := []struct{ name, format, body, field, key, alias string }{
		{"OpenAI", "openai", `{"object":"list","data":[{"id":"gpt-6.1-sol","created":9007199254740993,"owned_by":"codex"},{"id":"native"},{"id":"other"},{"id":"offline"}]}`, "data", "id", "friendly"},
		{"Claude", "claude", `{"data":[{"id":"` + ensureClaudeID("gpt-6.1-sol") + `","display_name":"Target","nested":{"capability":true}},{"id":"` + ensureClaudeID("native") + `"}],"first_id":"old","last_id":"old","has_more":true}`, "data", "id", ensureClaudeID("friendly")},
		{"Codex slug despite OpenAI format", "openai", `{"models":[{"slug":"gpt-6.1-sol","context_window":272000},{"slug":"native"},{"slug":"other"}]}`, "models", "slug", "friendly"},
		{"Gemini", "gemini", `{"models":[{"name":"models/gpt-6.1-sol","supportedGenerationMethods":["generateContent"]},{"name":"models/native"},{"name":"models/other"}]}`, "models", "name", "models/friendly"},
		{"Grok Shell", "openai", `{"object":"list","data":[{"id":"gpt-6.1-sol","model":"gpt-6.1-sol","name":"GPT","api_backend":"responses","context_window":128000},{"id":"native","model":"native"},{"id":"other","model":"other"}]}`, "data", "id", "friendly"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := hooksFor(t, fixture("gpt-6.1-sol", true))
			body := []byte(tc.body)
			out := h.Response(pluginapi.ResponseInterceptRequest{RequestID: tc.name, SourceFormat: tc.format, Body: body, StatusCode: 200})
			var got map[string]json.RawMessage
			if json.Unmarshal(out.Body, &got) != nil {
				t.Fatal(string(out.Body))
			}
			var entries []map[string]json.RawMessage
			_ = json.Unmarshal(got[tc.field], &entries)
			if len(entries) != 2 {
				t.Fatalf("expected native and alias only: %s", out.Body)
			}
			if stringField(entries[1], tc.key) != tc.alias {
				t.Fatalf("alias not published in correct shape: %s", out.Body)
			}
			var original map[string]json.RawMessage
			_ = json.Unmarshal(body, &original)
			var originals []map[string]json.RawMessage
			_ = json.Unmarshal(original[tc.field], &originals)
			for key, value := range originals[0] {
				if key != tc.key && key != "model" && !bytes.Equal(value, entries[1][key]) {
					t.Fatalf("target metadata %s lost: %s", key, out.Body)
				}
			}
			if tc.name == "Grok Shell" && stringField(entries[1], "model") != "friendly" {
				t.Fatal("Grok selection leaks hidden target")
			}
			if tc.format == "claude" && (string(got["first_id"]) != string(entries[0]["id"]) || string(got["last_id"]) != string(entries[1]["id"]) || string(got["has_more"]) != "false") {
				t.Fatal("Claude page bounds stale")
			}
			if string(body) != tc.body {
				t.Fatal("read-only catalog mutated")
			}
		})
	}
}

func TestCatalogFailClosedAndNoFabricatedAliases(t *testing.T) {
	for _, tc := range []struct{ format, body string }{
		{"openai", `{"data":[{"id":"gpt-6.1-sol"}]}`}, {"claude", `{"data":[],"first_id":"","last_id":"","has_more":false}`}, {"gemini", `{"models":[]}`}, {"openai", `{"models":[]}`}, {"openai", `malformed`},
	} {
		h := New(func() *policy.Snapshot { return nil })
		out := h.Response(pluginapi.ResponseInterceptRequest{SourceFormat: tc.format, Body: []byte(tc.body)})
		if !json.Valid(out.Body) || bytes.Contains(out.Body, []byte("gpt-6.1-sol")) || len(out.Body) == 0 {
			t.Fatalf("catalog failed open: %s", out.Body)
		}
		h.Close()
	}
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	out := h.Response(pluginapi.ResponseInterceptRequest{SourceFormat: "openai", Body: []byte(`{"data":[{"id":"native"}]}`)})
	if bytes.Contains(out.Body, []byte("friendly")) {
		t.Fatal("alias invented without target metadata")
	}
	s := fixture("gpt-6.1-sol", false)
	state := s.StateCopy()
	state.Aliases[0].Provider = "claude"
	unroutable := hooksFor(t, policy.New(state, s.CatalogCopy(), true))
	out = unroutable.Response(pluginapi.ResponseInterceptRequest{SourceFormat: "openai", Body: []byte(`{"data":[{"id":"gpt-6.1-sol"}]}`)})
	if bytes.Contains(out.Body, []byte("friendly")) {
		t.Fatal("provider pin ignored in alias list")
	}
}

func TestFrontendGuardDoesNotAuthenticate(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", false))
	for _, path := range []string{"/v1/realtime", "/v1/realtime/client_secrets", "/v1/live", "/v1/live/call_1", "/v1/%72ealtime"} {
		out := h.Frontend(pluginapi.FrontendAuthRequest{Path: path, Headers: http.Header{"Authorization": {"Bearer secret"}}})
		if out.Authenticated || out.DenyStatusCode != 403 {
			t.Fatalf("uncovered path accepted: %s %+v", path, out)
		}
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/models", "/v1/realtime-other"} {
		out := h.Frontend(pluginapi.FrontendAuthRequest{Path: path})
		if out.Authenticated || out.DenyStatusCode != 0 || out.DenyMessage != "" {
			t.Fatalf("guard interfered with normal authentication: %+v", out)
		}
	}
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		if got := h.Frontend(pluginapi.FrontendAuthRequest{Path: path, Body: []byte(`{"model":"gpt-6.1-sol"}`)}); got.DenyStatusCode != 404 {
			t.Fatalf("Alpha hidden direct model: %+v", got)
		}
		if got := h.Frontend(pluginapi.FrontendAuthRequest{Path: path, Body: []byte("compressed"), DecodedBody: []byte(`{"model":"friendly"}`)}); got.DenyStatusCode != 0 || got.Authenticated {
			t.Fatalf("Alpha decoded alias: %+v", got)
		}
	}
	h.Guard = false
	if out := h.Frontend(pluginapi.FrontendAuthRequest{Path: "/v1/realtime"}); out.DenyStatusCode != 0 || out.Authenticated {
		t.Fatal("guard flag ignored")
	}
}

func TestPanicsNeverEscapeHooks(t *testing.T) {
	h := New(func() *policy.Snapshot { panic("secret should not escape") })
	t.Cleanup(h.Close)
	request := pluginapi.RequestInterceptRequest{RequestID: "panic", RequestedModel: "native", Model: "native", SourceFormat: "openai"}
	assertTermination(t, h.Before(request), 503)
	assertTermination(t, h.After(request), 503)
	if out := h.Route(pluginapi.ModelRouteRequest{SourceFormat: "codex-alpha-search"}); !out.Handled || out.TargetKind != pluginapi.ModelRouteTargetSelf {
		t.Fatal("route panic failed open")
	}
	for _, req := range []pluginapi.ResponseInterceptRequest{{RequestedModel: "native", Model: "native"}, {SourceFormat: "gemini"}} {
		out := h.Response(req)
		if !json.Valid(out.Body) || bytes.Contains(out.Body, []byte("secret")) {
			t.Fatal("response panic leaked input")
		}
	}
	if out := h.Stream(pluginapi.StreamChunkInterceptRequest{RequestedModel: "native", Model: "native", Body: []byte(`{"secret":true}`)}); !out.DropChunk {
		t.Fatal("stream panic failed open")
	}
	if out := h.Frontend(pluginapi.FrontendAuthRequest{Path: "/v1/realtime"}); out.DenyStatusCode != 503 {
		t.Fatal("frontend panic failed open")
	}
	h.Complete(pluginapi.RequestCompletion{RequestID: "panic"})
}

func TestConcurrentRequestsAndPublication(t *testing.T) {
	var current atomic.Pointer[policy.Snapshot]
	current.Store(fixture("gpt-6.1-sol", true))
	h := New(current.Load)
	t.Cleanup(h.Close)
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				id := fmt.Sprintf("%d/%d", worker, i)
				req := pluginapi.RequestInterceptRequest{RequestID: id, RequestedModel: "native", Model: "native"}
				if h.Before(req).Terminate || h.After(req).Terminate {
					t.Errorf("native request rejected during publication")
				}
				h.Response(pluginapi.ResponseInterceptRequest{RequestID: id, RequestedModel: "native", Model: "native", Body: []byte(`{"model":"native"}`)})
				h.Complete(pluginapi.RequestCompletion{RequestID: id})
			}
		}(worker)
	}
	for i := 0; i < 40; i++ {
		next := fixture("gpt-6.2-sol", true)
		h.Update(next)
		current.Store(next)
	}
	wg.Wait()
	h.mu.Lock()
	count := len(h.pins)
	h.mu.Unlock()
	if count != 0 {
		t.Fatalf("completed requests leaked %d pins", count)
	}
}

func TestExpiredPreviousSnapshotCannotAdmit(t *testing.T) {
	var current atomic.Pointer[policy.Snapshot]
	current.Store(fixture("gpt-6.1-sol", true))
	h := New(current.Load)
	t.Cleanup(h.Close)
	next := fixture("gpt-6.2-sol", true)
	h.Update(next)
	current.Store(next)
	h.mu.Lock()
	h.previousUntil = time.Now().Add(-time.Second)
	h.mu.Unlock()
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "expired", RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 503)
}

func TestPreviousGenerationCannotRepublishRevokedAlias(t *testing.T) {
	var current atomic.Pointer[policy.Snapshot]
	first := fixture("gpt-6.1-sol", true)
	current.Store(first)
	h := New(current.Load)
	t.Cleanup(h.Close)
	pinned := pluginapi.RequestInterceptRequest{RequestID: "admitted", RequestedModel: "friendly", Model: "gpt-6.1-sol"}
	assertTermination(t, h.Before(pinned), 0)
	second := fixture("gpt-6.2-sol", true)
	h.Update(second)
	current.Store(second)
	state := second.StateCopy()
	state.Rules = state.Rules[1:]
	next := policy.New(state, second.CatalogCopy(), true)
	h.Update(next)
	current.Store(next)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "revoked", RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 403)
	assertTermination(t, h.After(pinned), 0)
}

func TestCatalogOnlyPublicationRetainsPreviousAliasTarget(t *testing.T) {
	var current atomic.Pointer[policy.Snapshot]
	first := fixture("gpt-6.1-sol", true)
	current.Store(first)
	h := New(current.Load)
	t.Cleanup(h.Close)
	route := h.Route(pluginapi.ModelRouteRequest{RequestedModel: "friendly", AvailableProviders: []string{"codex"}})
	second := fixture("gpt-6.2-sol", true)
	h.Update(second)
	current.Store(second)
	catalog := second.CatalogCopy()
	catalog.RefreshedAt = time.Now()
	catalog.Models[0].CredentialCount = 5
	refreshed := policy.New(second.StateCopy(), catalog, true)
	h.Update(refreshed)
	current.Store(refreshed)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "refresh-race", RequestedModel: "friendly", Model: route.TargetModel}), 0)
	h.mu.Lock()
	previous, previousUntil := h.previous, h.previousUntil
	h.mu.Unlock()
	if previous != first {
		t.Fatal("catalog-only publication discarded alias target generation")
	}
	// An unchanged map must not renew the grace period on every background poll.
	h.mu.Lock()
	h.previousUntil = time.Now().Add(-time.Second)
	h.mu.Unlock()
	again := policy.New(refreshed.StateCopy(), catalog, true)
	h.Update(again)
	current.Store(again)
	h.mu.Lock()
	afterRefresh := h.previousUntil
	h.mu.Unlock()
	if !afterRefresh.Before(previousUntil) {
		t.Fatal("catalog-only publication renewed previous generation TTL")
	}
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "expired-refresh-race", RequestedModel: "friendly", Model: route.TargetModel}), 503)
}

func TestDisabledAliasCannotUsePreviousTargetGeneration(t *testing.T) {
	var current atomic.Pointer[policy.Snapshot]
	first := fixture("gpt-6.1-sol", true)
	current.Store(first)
	h := New(current.Load)
	t.Cleanup(h.Close)
	second := fixture("gpt-6.2-sol", true)
	h.Update(second)
	current.Store(second)
	state := second.StateCopy()
	state.Aliases[0].Enabled = false
	disabled := policy.New(state, second.CatalogCopy(), true)
	h.Update(disabled)
	current.Store(disabled)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "disabled", RequestedModel: "friendly", Model: "gpt-6.2-sol"}), 403)
}

func TestWhitelistOffStillHidesOriginalAndExactAllowRestoresIt(t *testing.T) {
	s := fixture("gpt-6.1-sol", true)
	state := s.StateCopy()
	state.Settings.WhitelistEnabled = false
	state.Rules = nil
	h := hooksFor(t, policy.New(state, s.CatalogCopy(), true))
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "hidden", RequestedModel: "gpt-6.1-sol", Model: "gpt-6.1-sol"}), 404)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "alias", RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 0)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "open", RequestedModel: "other", Model: "other"}), 0)
	if out := h.Frontend(pluginapi.FrontendAuthRequest{Path: "/v1/realtime"}); out.DenyStatusCode != 0 || out.Authenticated {
		t.Fatal("unrestricted whitelist blocked realtime")
	}
	state.Rules = []policy.Rule{{ID: "exact", Enabled: true, Kind: "exact", Value: "gpt-6.1-sol"}}
	explicit := hooksFor(t, policy.New(state, s.CatalogCopy(), true))
	assertTermination(t, explicit.Before(pluginapi.RequestInterceptRequest{RequestID: "exact", RequestedModel: "gpt-6.1-sol", Model: "gpt-6.1-sol"}), 0)
}

func TestCodexAliasRewritesOptionalDetailID(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	out := h.Response(pluginapi.ResponseInterceptRequest{SourceFormat: "openai", Body: []byte(`{"models":[{"slug":"gpt-6.1-sol","id":"gpt-6.1-sol"}]}`)})
	var catalog struct{ Models []struct{ ID, Slug string } }
	if json.Unmarshal(out.Body, &catalog) != nil || len(catalog.Models) != 1 || catalog.Models[0].ID != "friendly" || catalog.Models[0].Slug != "friendly" {
		t.Fatalf("detail selector could expose hidden target: %s", out.Body)
	}
}

func TestIdleRequestPinsExpireWithoutFurtherTraffic(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", true))
	h.ttl = 15 * time.Millisecond
	h.Before(pluginapi.RequestInterceptRequest{RequestID: "idle", RequestedModel: "friendly", Model: "gpt-6.1-sol"})
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("idle request pin did not expire")
		case <-tick.C:
			h.mu.Lock()
			count := len(h.pins)
			h.mu.Unlock()
			if count == 0 {
				return
			}
		}
	}
}

func TestMismatchedRequestIDCannotReuseAdmission(t *testing.T) {
	h := hooksFor(t, fixture("gpt-6.1-sol", false))
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "same", RequestedModel: "native", Model: "native"}), 0)
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestID: "same", RequestedModel: "other", Model: "other"}), 503)
	if out := h.Response(pluginapi.ResponseInterceptRequest{RequestID: "same", RequestedModel: "other", Model: "other", Body: []byte(`{"secret":true}`)}); bytes.Contains(out.Body, []byte("secret")) {
		t.Fatal("cross-request response passed through")
	}
}

func TestUnavailableAndMissingAliasTarget(t *testing.T) {
	s := fixture("gpt-6.1-sol", true)
	catalog := s.CatalogCopy()
	catalog.Models = nil
	h := hooksFor(t, policy.New(s.StateCopy(), catalog, true))
	assertTermination(t, h.Before(pluginapi.RequestInterceptRequest{RequestedModel: "friendly", Model: "gpt-6.1-sol"}), 503)
	invalid := New(func() *policy.Snapshot { return policy.New(policy.DefaultState(), policy.Catalog{}, false) })
	t.Cleanup(invalid.Close)
	assertTermination(t, invalid.Before(pluginapi.RequestInterceptRequest{RequestedModel: "anything", Model: "anything"}), 503)
	if out := invalid.Frontend(pluginapi.FrontendAuthRequest{Path: "/v1/live"}); out.DenyStatusCode != 503 {
		t.Fatal("unavailable policy did not deny uncovered path")
	}
}

func TestRewritePreservesNonModelText(t *testing.T) {
	body, err := rewriteModel([]byte(`{"model":"target","instructions":"target","metadata":{"model":"target"},"response":{"model":"target","output":[{"model":"target"}]}}`), "openai-response", "friendly")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), `"model":"friendly"`) != 2 || strings.Count(string(body), `"model":"target"`) != 2 {
		t.Fatal(string(body))
	}
}
