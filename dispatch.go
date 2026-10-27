package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/suk-6/cpa-model-dock/internal/hostabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type runtime struct {
	sync.RWMutex
	app    *application
	dir    string
	schema uint32
	host   hostabi.Caller
	guard  bool
}

func newRuntime(host hostabi.Caller) *runtime { return &runtime{host: host} }
func (r *runtime) Close() {
	r.cancelWork()
	r.Lock()
	defer r.Unlock()
	if r.app != nil {
		r.app.close()
	}
	r.app = nil
}

// Do not wait for management readers to finish paid probes before cancelling
// them. Copy the owner under a read lock; cancellation itself never calls the host.
func (r *runtime) cancelWork() {
	r.RLock()
	a := r.app
	r.RUnlock()
	if a != nil {
		a.cancelWork()
	}
}

// Dispatch exercises exactly the same JSON envelopes as the native C entrypoint.
// Hook failures are represented by successful envelopes with explicit denial
// decisions because CPA logs and ignores failed interceptor envelopes.
func (r *runtime) Dispatch(method string, raw []byte) (encoded []byte) {
	defer func() {
		if recover() != nil {
			if denied, ok := failedHook(method); ok {
				encoded = okEnvelope(denied)
			} else {
				encoded = errorEnvelope("plugin_error", "plugin operation failed", 500)
			}
		}
	}()
	if method == pluginabi.MethodPluginRegister || method == pluginabi.MethodPluginReconfigure {
		result, e := r.register(raw)
		if e != nil {
			return errorEnvelope("configuration_error", e.Error(), 500)
		}
		return okEnvelope(result)
	}
	if method == pluginabi.MethodPluginShutdown || method == pluginabi.MethodPluginQuiesce {
		r.Close()
		return okEnvelope(struct{}{})
	}
	r.RLock()
	defer r.RUnlock()
	if r.app == nil {
		if v, ok := failedHook(method); ok {
			return okEnvelope(v)
		}
		return errorEnvelope("policy_unavailable", "plugin is inactive", 503)
	}
	a := r.app
	switch method {
	case pluginabi.MethodModelRoute:
		var req pluginapi.ModelRouteRequest
		if json.Unmarshal(raw, &req) != nil {
			return okEnvelope(failedRoute())
		}
		return okEnvelope(a.hooks.Route(req))
	case pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter:
		var req pluginapi.RequestInterceptRequest
		if json.Unmarshal(raw, &req) != nil {
			return okEnvelope(failedRequest())
		}
		if method == pluginabi.MethodRequestInterceptBefore {
			return okEnvelope(a.hooks.Before(req))
		}
		return okEnvelope(a.hooks.After(req))
	case pluginabi.MethodResponseInterceptAfter:
		var req pluginapi.ResponseInterceptRequest
		if json.Unmarshal(raw, &req) != nil {
			return okEnvelope(pluginapi.ResponseInterceptResponse{Body: failureBody()})
		}
		return okEnvelope(a.hooks.Response(req))
	case pluginabi.MethodResponseInterceptStreamChunk:
		var req pluginapi.StreamChunkInterceptRequest
		if json.Unmarshal(raw, &req) != nil {
			return okEnvelope(pluginapi.StreamChunkInterceptResponse{DropChunk: true})
		}
		return okEnvelope(a.hooks.Stream(req))
	case pluginabi.MethodRequestComplete:
		var req pluginapi.RequestCompletion
		if json.Unmarshal(raw, &req) == nil {
			a.hooks.Complete(req)
		}
		return okEnvelope(struct{}{})
	case pluginabi.MethodFrontendAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginID})
	case pluginabi.MethodFrontendAuthAuthenticate:
		var req pluginapi.FrontendAuthRequest
		if json.Unmarshal(raw, &req) != nil {
			return okEnvelope(pluginapi.FrontendAuthResponse{DenyStatusCode: 403, DenyMessage: "Model policy denied request"})
		}
		return okEnvelope(a.hooks.Frontend(req))
	case pluginabi.MethodManagementRegister:
		var req pluginapi.ManagementRegistrationRequest
		if json.Unmarshal(raw, &req) != nil {
			return errorEnvelope("invalid_request", "invalid management registration", 400)
		}
		return okEnvelope(a.management(r.schema, r.guard).Register(req))
	case pluginabi.MethodManagementHandle:
		var req pluginapi.ManagementRequest
		if json.Unmarshal(raw, &req) != nil {
			return errorEnvelope("invalid_request", "invalid management request", 400)
		}
		return okEnvelope(a.management(r.schema, r.guard).Handle(context.Background(), req))
	case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream, pluginabi.MethodExecutorCountTokens, pluginabi.MethodExecutorHTTPRequest:
		// A static denial executor makes the self route valid at the host's routing
		// precheck. Alpha Search rejects that route with 503 before calling it.
		// Ordinary unavailable aliases terminate in BeforeAuth first.
		return errorEnvelope("model_not_allowed", "Model policy denied request", 503)
	default:
		return errorEnvelope("unknown_method", "unsupported plugin method", 400)
	}
}
func (r *runtime) register(raw []byte) (any, error) {
	var req struct {
		ConfigYAML    []byte `json:"config_yaml"`
		SchemaVersion uint32 `json:"schema_version"`
	}
	if json.Unmarshal(raw, &req) != nil {
		return nil, errors.New("invalid registration")
	}
	if req.SchemaVersion < 6 {
		return nil, errors.New("host schema 6 or newer required")
	}
	cfg, interval, e := parseConfig(req.ConfigYAML)
	if e != nil {
		return nil, e
	}
	r.cancelWork()
	r.Lock()
	defer r.Unlock()
	schema := min(req.SchemaVersion, pluginabi.SchemaVersion)
	if r.app == nil || cfg.StateDir != r.dir {
		next, err := newApplication(cfg.StateDir, r.host)
		if err != nil {
			// Validation succeeded but opening the replacement failed. Resume the
			// existing lifecycle after the pre-lock cancellation, preserving policy.
			if r.app != nil {
				r.app.start(r.app.baseInterval)
			}
			return nil, errors.New("cannot open plugin state directory")
		}
		if r.app != nil {
			// Drain the old writer, but preserve per-request pins across a directory
			// reconfiguration. Both generations' hooks read this shared atomic pointer.
			r.app.stop()
			candidate := next.current.Load()
			next.hooks.Close()
			next.current = r.app.current
			next.hooks = r.app.hooks
			next.publish(candidate)
		}
		r.app = next
		r.dir = cfg.StateDir
	}
	r.schema = schema
	r.guard = cfg.guard()
	r.app.hooks.Guard = r.guard
	r.app.start(interval)
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  map[string]any     `json:"capabilities"`
	}{schema, metadata(), map[string]any{
		"model_router": true, "request_interceptor": true, "response_interceptor": true, "response_stream_interceptor": true, "request_lifecycle_plugin": true, "frontend_auth_provider": true, "frontend_auth_provider_exclusive": false, "management_api": true,
		"executor": true, "executor_model_scope": "static", "executor_input_formats": []string{"codex-alpha-search", "openai", "openai-response", "claude", "gemini"}, "executor_output_formats": []string{"codex-alpha-search", "openai", "openai-response", "claude", "gemini"},
	}}, nil
}
func metadata() pluginapi.Metadata {
	return pluginapi.Metadata{Name: "CPAModelDock", Version: pluginVersion, Author: "CPAModelDock contributors", GitHubRepository: "https://github.com/suk-6/cpa-model-dock", ConfigFields: []pluginapi.ConfigField{
		{Name: "state-dir", Type: pluginapi.ConfigFieldTypeString, Default: "", Description: "Absolute directory for plugin state and history; defaults to $HOME/.cli-proxy-api/cpa-model-dock"},
		{Name: "catalog-refresh-interval", Type: pluginapi.ConfigFieldTypeString, Default: "5m", Description: "Catalog refresh interval, minimum 30s"},
		{Name: "guard-uncovered-endpoints", Type: pluginapi.ConfigFieldTypeBoolean, Default: true, Description: "Guard Alpha Search and realtime/live where CPA invokes the plugin"},
	}}
}
func okEnvelope(result any) []byte {
	b, e := json.Marshal(result)
	if e != nil {
		return errorEnvelope("serialization_error", "cannot encode response", 500)
	}
	out, _ := json.Marshal(pluginabi.Envelope{OK: true, Result: b})
	return out
}
func errorEnvelope(code, message string, status int) []byte {
	b, _ := pluginabi.NewErrorEnvelope(code, message, status)
	return b
}
func failureBody() []byte {
	return []byte(`{"error":{"type":"permission_error","code":"policy_unavailable","message":"Model policy unavailable"}}`)
}
func failedRequest() pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: 503, ResponseBody: failureBody()}
}
func failedRoute() pluginapi.ModelRouteResponse {
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Target: pluginID, Reason: "policy_unavailable"}
}
func failedHook(method string) (any, bool) {
	switch method {
	case pluginabi.MethodRequestInterceptBefore, pluginabi.MethodRequestInterceptAfter:
		return failedRequest(), true
	case pluginabi.MethodModelRoute:
		return failedRoute(), true
	case pluginabi.MethodResponseInterceptAfter:
		return pluginapi.ResponseInterceptResponse{Body: failureBody()}, true
	case pluginabi.MethodResponseInterceptStreamChunk:
		return pluginapi.StreamChunkInterceptResponse{DropChunk: true}, true
	case pluginabi.MethodFrontendAuthAuthenticate:
		return pluginapi.FrontendAuthResponse{DenyStatusCode: 503, DenyMessage: "Model policy unavailable"}, true
	case pluginabi.MethodRequestComplete:
		return struct{}{}, true
	}
	return nil, false
}
