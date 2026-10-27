// Package hooks implements CPA's policy hooks without returning transport errors
// for policy denials. CPA ignores interceptor errors, so denials are response values.
package hooks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const requestTTL = 10 * time.Minute

// Hooks is safe for concurrent hook calls. Set Guard before exposing the hooks
// to CPA. A true Guard enables protection of otherwise uncovered HTTP endpoints.
type Hooks struct {
	Guard         bool
	current       func() *policy.Snapshot
	mu            sync.Mutex
	previous      *policy.Snapshot
	previousUntil time.Time
	pins          map[string]*requestPin
	ttl           time.Duration
}

type requestPin struct {
	snapshot  *policy.Snapshot
	requested string
	decision  policy.Decision
	rewrite   bool
	timer     *time.Timer // guarded by Hooks.mu
	expiresAt time.Time   // guarded by Hooks.mu
	mu        sync.Mutex
	failed    bool
	errorSent bool
	pending   []byte
}

func New(current func() *policy.Snapshot) *Hooks {
	return &Hooks{Guard: true, current: current, pins: make(map[string]*requestPin), ttl: requestTTL}
}

// Update must run immediately before publishing next through the current
// callback. The router has no RequestID; Before can therefore accept a route
// selected from the immediately preceding alias routing generation. Publications
// that only change catalog facts, rules, or rewrite settings retain that generation.
func (h *Hooks) Update(next *policy.Snapshot) {
	if h == nil {
		return
	}
	old := h.latest()
	h.mu.Lock()
	defer h.mu.Unlock()
	if old != next && !sameAliasRoutes(old, next) {
		h.previous, h.previousUntil = old, time.Now().Add(requestTTL)
	}
}

func sameAliasRoutes(a, b *policy.Snapshot) bool {
	type destination struct{ target, provider string }
	routes := func(s *policy.Snapshot) map[string]destination {
		result := make(map[string]destination)
		if s == nil {
			return result
		}
		for _, alias := range s.StateCopy().Aliases {
			if alias.Enabled {
				result[s.Normalize(alias.Name)] = destination{alias.Target, strings.ToLower(alias.Provider)}
			}
		}
		return result
	}
	left, right := routes(a), routes(b)
	if len(left) != len(right) {
		return false
	}
	for name, target := range left {
		if other, exists := right[name]; !exists || other != target {
			return false
		}
	}
	return true
}

// Close releases request pins and their idle-expiry timers at plugin shutdown.
func (h *Hooks) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, p := range h.pins {
		p.timer.Stop()
		delete(h.pins, id)
	}
	h.previous = nil
}

func (h *Hooks) latest() *policy.Snapshot {
	if h == nil || h.current == nil {
		return nil
	}
	return h.current()
}

func unavailable() policy.Decision {
	return policy.Decision{Status: 503, Code: "policy_unavailable", Reason: "Model policy is unavailable"}
}

func decide(s *policy.Snapshot, name string) policy.Decision {
	if s == nil || !s.Available() {
		return unavailable()
	}
	return s.Decide(name)
}

func requestedName(requested, model string) string {
	if strings.TrimSpace(requested) != "" {
		return requested
	}
	return model
}

func aliasDestination(s *policy.Snapshot, d policy.Decision, model string) bool {
	return s != nil && d.Allowed && d.Alias != nil && s.Normalize(model) == s.Normalize(d.ResolvedTarget)
}

func (h *Hooks) pin(id, requested, model string, before bool) *requestPin {
	if h == nil {
		return &requestPin{decision: unavailable()}
	}
	h.mu.Lock()
	if p := h.pins[id]; id != "" && p != nil {
		h.touch(p)
		h.mu.Unlock()
		return p
	}
	h.mu.Unlock()
	s := h.latest()
	h.mu.Lock()
	previous, until := h.previous, h.previousUntil
	h.mu.Unlock()
	d := decide(s, requested)
	// An alias router may have selected the old target just before publication.
	// Only BeforeAuth can establish this binding; later hooks cannot switch it.
	// The current policy must still publish this alias: an old routing generation
	// cannot restore permission that was revoked before admission.
	if before && d.Allowed && d.Alias != nil && !aliasDestination(s, d, model) && previous != nil && time.Now().Before(until) {
		old := decide(previous, requested)
		if aliasDestination(previous, old, model) {
			s, d = previous, old
		}
	}
	p := &requestPin{snapshot: s, requested: requested, decision: d}
	if s != nil && d.Alias != nil {
		p.rewrite = s.StateCopy().Settings.RewriteResponseModel
		if override := d.Alias.RewriteResponseModel; override != nil {
			p.rewrite = *override
		}
	}
	if id == "" {
		return p
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing := h.pins[id]; existing != nil {
		h.touch(existing)
		return existing
	}
	h.pins[id] = p
	p.expiresAt = time.Now().Add(h.ttl)
	p.timer = time.AfterFunc(h.ttl, func() { h.expire(id, p) })
	return p
}

// touch and expiry decisions run under mu. Reset alone is insufficient: an old
// timer callback might already be waiting for mu when a live request touches it.
func (h *Hooks) touch(p *requestPin) {
	p.expiresAt = time.Now().Add(h.ttl)
	p.timer.Reset(h.ttl)
}

func (h *Hooks) expire(id string, p *requestPin) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pins[id] != p {
		return
	}
	if remaining := time.Until(p.expiresAt); remaining > 0 {
		p.timer.Reset(remaining)
		return
	}
	delete(h.pins, id)
}

// The parent registers a static denial executor for this self route. It never
// forwards requests. Alpha rejects it with 503; ordinary requests reach
// BeforeAuth even when the missing alias has no host registry entry.
func sentinel() pluginapi.ModelRouteResponse {
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetSelf, Reason: "model_not_allowed"}
}

func (h *Hooks) Route(req pluginapi.ModelRouteRequest) (out pluginapi.ModelRouteResponse) {
	defer func() {
		if recover() != nil {
			out = sentinel()
		}
	}()
	s := h.latest()
	d := decide(s, req.RequestedModel)
	alpha := req.SourceFormat == "codex-alpha-search"
	if !d.Allowed {
		if alpha && h.Guard {
			return sentinel()
		}
		if !alpha && d.Code == "alias_target_unavailable" {
			// Provider lookup precedes BeforeAuth in CPA. Without a valid
			// route, a removed target yields generic 400 model_not_found
			// instead of the contract's 503 alias_target_unavailable.
			return sentinel()
		}
		return out // BeforeAuth supplies a real protocol denial on ordinary routes.
	}
	if d.Alias == nil {
		return out
	}
	provider := d.Provider
	if provider == "" && s != nil {
		providers := []string{}
		for _, m := range s.CatalogCopy().Models {
			if m.Routable && s.Normalize(m.ID) == s.Normalize(d.ResolvedTarget) {
				providers = append(providers, m.Providers...)
			}
		}
		sort.Strings(providers)
		for _, candidate := range providers {
			if containsFold(req.AvailableProviders, candidate) && (!alpha || strings.EqualFold(candidate, "codex")) {
				provider = candidate
				break
			}
		}
	}
	if provider == "" || !containsFold(req.AvailableProviders, provider) || (alpha && !strings.EqualFold(provider, "codex")) {
		if alpha && h.Guard {
			return sentinel()
		}
		return out
	}
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: provider, TargetModel: d.ResolvedTarget + thinkingSuffix(req.RequestedModel), Reason: "alias"}
}

func containsFold(values []string, value string) bool {
	for _, candidate := range values {
		if strings.EqualFold(candidate, value) {
			return true
		}
	}
	return false
}

func thinkingSuffix(model string) string {
	model = strings.TrimSpace(model)
	if i := strings.LastIndex(model, "("); i > 0 && strings.HasSuffix(model, ")") {
		return model[i:]
	}
	return ""
}

func (h *Hooks) Before(req pluginapi.RequestInterceptRequest) (out pluginapi.RequestInterceptResponse) {
	defer func() {
		if recover() != nil {
			out = terminate(req.SourceFormat, unavailable())
		}
	}()
	name := requestedName(req.RequestedModel, req.Model)
	p := h.pin(req.RequestID, name, req.Model, true)
	if !sameRequest(p, name) {
		return terminate(req.SourceFormat, unavailable())
	}
	if !p.decision.Allowed {
		return terminate(req.SourceFormat, p.decision)
	}
	if p.decision.Alias != nil {
		if !aliasDestination(p.snapshot, p.decision, req.Model) {
			return terminate(req.SourceFormat, policy.Decision{Status: 503, Code: "alias_target_unavailable"})
		}
		if len(bytes.TrimSpace(req.Body)) > 0 {
			body, err := rewriteRequest(req.Body, req.Model)
			if err != nil {
				return terminate(req.SourceFormat, unavailable())
			}
			out.Body = body
		}
	}
	return out
}

func sameRequest(p *requestPin, name string) bool {
	return p.snapshot != nil && p.snapshot.Normalize(p.requested) == p.snapshot.Normalize(name)
}

func (h *Hooks) After(req pluginapi.RequestInterceptRequest) (out pluginapi.RequestInterceptResponse) {
	defer func() {
		if recover() != nil {
			out = terminate(req.SourceFormat, unavailable())
		}
	}()
	name := requestedName(req.RequestedModel, req.Model)
	p := h.pin(req.RequestID, name, req.Model, false)
	if !sameRequest(p, name) {
		return terminate(req.SourceFormat, unavailable())
	}
	if !p.decision.Allowed {
		return terminate(req.SourceFormat, p.decision)
	}
	if p.decision.Alias != nil {
		// CPA supplies neither selected-auth provenance nor verified credential
		// mapping facts here. ToFormat and arbitrary Metadata are not evidence.
		if !aliasDestination(p.snapshot, p.decision, req.Model) {
			return terminate(req.SourceFormat, policy.Decision{Status: 403, Code: "model_not_allowed"})
		}
	} else {
		d := p.snapshot.DecideResolved(req.Model)
		if !d.Allowed {
			return terminate(req.SourceFormat, d)
		}
	}
	return out
}

func (h *Hooks) Complete(req pluginapi.RequestCompletion) {
	defer func() { _ = recover() }()
	if h == nil || req.RequestID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if p := h.pins[req.RequestID]; p != nil {
		p.timer.Stop()
		delete(h.pins, req.RequestID)
	}
}

func (h *Hooks) Frontend(req pluginapi.FrontendAuthRequest) (out pluginapi.FrontendAuthResponse) {
	defer func() {
		if recover() != nil {
			out = pluginapi.FrontendAuthResponse{DenyStatusCode: 503, DenyMessage: "Model policy is unavailable"}
		}
	}()
	if h == nil || !h.Guard {
		return out
	}
	path, err := url.PathUnescape(req.Path)
	if err != nil {
		return pluginapi.FrontendAuthResponse{DenyStatusCode: 403, DenyMessage: "Model is not allowed"}
	}
	uncovered := endpoint(path, "/v1/realtime") || endpoint(path, "/v1/live")
	alpha := path == "/v1/alpha/search" || path == "/backend-api/codex/alpha/search"
	if !uncovered && !alpha {
		return out
	}
	s := h.latest()
	if s == nil || !s.Available() {
		return pluginapi.FrontendAuthResponse{DenyStatusCode: 503, DenyMessage: "Model policy is unavailable"}
	}
	if uncovered && s.StateCopy().Settings.WhitelistEnabled {
		// Sideband call IDs/client secrets do not provide a verifiable model.
		return pluginapi.FrontendAuthResponse{DenyStatusCode: 403, DenyMessage: "Endpoint is unavailable while model policy is enabled"}
	}
	if alpha {
		body := req.Body
		if req.DecodedBody != nil {
			body = req.DecodedBody
		}
		var payload struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(body, &payload) != nil {
			return pluginapi.FrontendAuthResponse{DenyStatusCode: 403, DenyMessage: "Model is not allowed"}
		}
		d := decide(s, payload.Model)
		if !d.Allowed {
			return pluginapi.FrontendAuthResponse{DenyStatusCode: denialStatus(d), DenyMessage: safeMessage(d)}
		}
	}
	// No success identity: this is a nonexclusive guard, never an authenticator.
	return out
}

func endpoint(path, base string) bool { return path == base || strings.HasPrefix(path, base+"/") }

func denialStatus(d policy.Decision) int {
	if d.Status == 403 || d.Status == 404 || d.Status == 503 {
		return d.Status
	}
	return 403
}

func safeMessage(d policy.Decision) string {
	switch denialStatus(d) {
	case 404:
		return "Model not found"
	case 503:
		if d.Code == "alias_target_unavailable" {
			return "Alias target is unavailable"
		}
		return "Model policy is unavailable"
	default:
		return "Model is not allowed"
	}
}

func errorBody(format string, d policy.Decision) []byte {
	status, message := denialStatus(d), safeMessage(d)
	code, typ := "model_not_allowed", "permission_error"
	if status == 404 {
		code, typ = "model_not_found", "not_found_error"
	}
	if status == 503 {
		code, typ = "policy_unavailable", "api_error"
		if d.Code == "alias_target_unavailable" {
			code = d.Code
		}
	}
	var payload any
	switch {
	case strings.Contains(format, "claude") || format == "anthropic":
		payload = map[string]any{"type": "error", "error": map[string]any{"type": typ, "message": message}}
	case strings.Contains(format, "gemini"):
		name := "PERMISSION_DENIED"
		if status == 404 {
			name = "NOT_FOUND"
		}
		if status == 503 {
			name = "UNAVAILABLE"
		}
		payload = map[string]any{"error": map[string]any{"code": status, "status": name, "message": message}}
	default:
		payload = map[string]any{"error": map[string]any{"code": code, "type": typ, "message": message, "param": "model"}}
	}
	body, _ := json.Marshal(payload)
	return body
}

func terminate(format string, d policy.Decision) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{Terminate: true, StatusCode: denialStatus(d), ResponseHeaders: http.Header{"Content-Type": {"application/json"}, "Cache-Control": {"no-store"}}, ResponseBody: errorBody(format, d)}
}
