package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/suk-6/cpa-model-dock/internal/hostabi"
	"github.com/suk-6/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// The fake implements the real callback JSON envelopes, including the reduced
// host.auth.models wire shape rather than in-process SDK auth objects.
func fakeHost() hostabi.Client {
	return hostabi.Client{Transport: func(method string, _ []byte) ([]byte, error) {
		var result any
		switch method {
		case "host.auth.models":
			result = map[string]any{"credentials": []any{map[string]any{"id": "credential-1", "auth_index": "idx", "provider": "codex", "models": []string{"gpt-6.1-sol", "gpt-6.2-sol", "gpt-6-astra"}}}}
		case "host.auth.list":
			result = map[string]any{"files": []any{map[string]any{"id": "credential-1", "auth_index": "idx", "status": "active"}}}
		case "host.auth.get_runtime":
			result = map[string]any{"auth": map[string]any{"id": "credential-1", "auth_index": "idx", "status": "active"}}
		case "host.log":
			result = struct{}{}
		case "host.model.execute":
			result = map[string]any{"status_code": 200, "body": []byte(`{"model":"gpt-6.2-sol"}`)}
		default:
			return nil, fmt.Errorf("unexpected host method %s", method)
		}
		return okEnvelope(result), nil
	}}
}
func rpc(t *testing.T, r *runtime, method string, request any, result any) {
	t.Helper()
	b, e := json.Marshal(request)
	if e != nil {
		t.Fatal(e)
	}
	raw := r.Dispatch(method, b)
	var env pluginabi.Envelope
	if e = json.Unmarshal(raw, &env); e != nil || !env.OK {
		t.Fatalf("%s: %s (%v)", method, raw, e)
	}
	if result != nil {
		if e = json.Unmarshal(env.Result, result); e != nil {
			t.Fatalf("%s decode: %v", method, e)
		}
	}
}
func registerTest(t *testing.T, r *runtime, schema uint32, dir string) {
	t.Helper()
	var registration struct {
		SchemaVersion uint32             `json:"schema_version"`
		Capabilities  map[string]any     `json:"capabilities"`
		Metadata      pluginapi.Metadata `json:"metadata"`
	}
	rpc(t, r, "plugin.register", map[string]any{"schema_version": schema, "config_yaml": []byte("state-dir: " + dir + "\ncatalog-refresh-interval: 30s\n")}, &registration)
	if registration.SchemaVersion != min(schema, uint32(7)) || registration.Metadata.Name != "CPAModelDock" || registration.Capabilities["request_interceptor"] != true || registration.Capabilities["response_stream_interceptor"] != true || registration.Capabilities["executor"] != true {
		t.Fatal(registration)
	}
}
func TestJSONEnvelopeDispatchManagementAndHooks(t *testing.T) {
	r := newRuntime(fakeHost())
	defer r.Close()
	registerTest(t, r, 7, t.TempDir())
	r.app.refresh(context.Background())
	var registration struct {
		Routes    []struct{ Method, Path string } `json:"routes"`
		Resources []struct{ Path, Menu string }   `json:"resources"`
	}
	rpc(t, r, "management.register", pluginapi.ManagementRegistrationRequest{BasePath: "/v0/management", ResourceBasePath: "/v0/resource/plugins/cpa-model-dock"}, &registration)
	if len(registration.Routes) != 11 || len(registration.Resources) != 5 {
		t.Fatal(registration)
	}
	state := policy.DefaultState()
	state.Settings.WhitelistEnabled = true
	state.Aliases = []policy.Alias{{Name: "sol", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol", Provider: "codex"}}
	state.Rules = []policy.Rule{{Enabled: true, Kind: "alias", Value: "sol"}}
	b, _ := json.Marshal(state)
	var response pluginapi.ManagementResponse
	rpc(t, r, "management.handle", pluginapi.ManagementRequest{Method: "PUT", Path: "/v0/management/plugins/cpa-model-dock/state", Body: b}, &response)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(response.Body))
	}
	var saved policy.State
	if e := json.Unmarshal(response.Body, &saved); e != nil || saved.Revision != 1 || saved.Aliases[0].ID == "" {
		t.Fatal(e, string(response.Body))
	}
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			var route pluginapi.ModelRouteResponse
			rpc(t, r, "model.route", pluginapi.ModelRouteRequest{RequestedModel: "sol", SourceFormat: "openai", Stream: stream, AvailableProviders: []string{"codex"}}, &route)
			if !route.Handled || route.Target != "codex" || route.TargetModel != "gpt-6.1-sol" {
				t.Fatal(route)
			}
			var before pluginapi.RequestInterceptResponse
			req := pluginapi.RequestInterceptRequest{RequestID: fmt.Sprint("req-", stream), RequestedModel: "sol", Model: route.TargetModel, SourceFormat: "openai", Stream: stream}
			rpc(t, r, "request.intercept_before", req, &before)
			if before.Terminate {
				t.Fatal(before)
			}
			rpc(t, r, "request.intercept_after", req, &before)
			if before.Terminate {
				t.Fatal(before)
			}
			rpc(t, r, "request.complete", pluginapi.RequestCompletion{RequestID: req.RequestID}, nil)
		})
	}
	var denied pluginapi.RequestInterceptResponse
	rpc(t, r, "request.intercept_before", pluginapi.RequestInterceptRequest{RequestID: "hidden", RequestedModel: "gpt-6.1-sol", Model: "gpt-6.1-sol", SourceFormat: "openai"}, &denied)
	if !denied.Terminate || denied.StatusCode != 404 {
		t.Fatal(denied)
	}
	// Stale PUT is denied and cannot change the immediately active hook policy.
	rpc(t, r, "management.handle", pluginapi.ManagementRequest{Method: "PUT", Path: "/v0/management/plugins/cpa-model-dock/state", Body: b}, &response)
	if response.StatusCode != 409 {
		t.Fatal(response.StatusCode, string(response.Body))
	}
	// Resource GET never becomes an alternative path to state, even with a query.
	rpc(t, r, "management.handle", pluginapi.ManagementRequest{Method: "GET", Path: "/v0/resource/plugins/cpa-model-dock/state"}, &response)
	if response.StatusCode != 404 {
		t.Fatal(response.StatusCode, string(response.Body))
	}
}
func TestSchemaNegotiation(t *testing.T) {
	for _, schema := range []uint32{6, 7, 99} {
		t.Run(fmt.Sprint(schema), func(t *testing.T) {
			r := newRuntime(fakeHost())
			defer r.Close()
			registerTest(t, r, schema, t.TempDir())
		})
	}
	r := newRuntime(fakeHost())
	defer r.Close()
	var env pluginabi.Envelope
	_ = json.Unmarshal(r.Dispatch("plugin.register", []byte(`{"schema_version":5}`)), &env)
	if env.OK {
		t.Fatal("accepted unsupported schema")
	}
}
func TestMalformedAndInactiveHookDeniesWithSuccessfulEnvelope(t *testing.T) {
	r := newRuntime(nil)
	var env pluginabi.Envelope
	_ = json.Unmarshal(r.Dispatch("request.intercept_before", []byte(`{`)), &env)
	var result pluginapi.RequestInterceptResponse
	_ = json.Unmarshal(env.Result, &result)
	if !env.OK || !result.Terminate || result.StatusCode != 503 {
		t.Fatal(string(env.Result))
	}
}
func TestConcurrentMutationUsesOneRevision(t *testing.T) {
	r := newRuntime(fakeHost())
	defer r.Close()
	registerTest(t, r, 7, t.TempDir())
	r.app.refresh(context.Background())
	state := policy.DefaultState()
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, _ := json.Marshal(state)
			raw := r.Dispatch("management.handle", mustJSON(pluginapi.ManagementRequest{Method: "PUT", Path: "/v0/management/plugins/cpa-model-dock/state", Body: b}))
			var env pluginabi.Envelope
			_ = json.Unmarshal(raw, &env)
			var out pluginapi.ManagementResponse
			_ = json.Unmarshal(env.Result, &out)
			results <- out.StatusCode
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
