// Package mgmt implements the authenticated plugin management API and static resources.
package mgmt

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/cpa-model-dock/cpa-model-dock/internal/store"
	"github.com/cpa-model-dock/cpa-model-dock/web"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	// BasePath is the exact authenticated management prefix.
	BasePath = "/v0/management/plugins/cpa-model-dock"
	// ResourceBasePath is the exact public static-resource prefix.
	ResourceBasePath = "/v0/resource/plugins/cpa-model-dock"
)

var assets = map[string]string{
	"/index.html": "text/html; charset=utf-8",
	"/app.js":     "text/javascript; charset=utf-8",
	"/i18n.js":    "text/javascript; charset=utf-8",
	"/app.css":    "text/css; charset=utf-8",
}

// Config connects management to the owner's atomic snapshot and persistence layer.
// Save and Restore must compare expected and publish the new snapshot before returning.
// Check must be side-effect free when dryRun is true. Mutation callbacks are serialized
// within this handler; their owners must also coordinate background writers.
type Config struct {
	Current    func() *policy.Snapshot
	Save       func(expected int64, state policy.State, actor, kind, summary string) (policy.State, error)
	Refresh    func(context.Context) policy.Catalog
	Check      func(context.Context, string, bool) (any, error)
	History    func(limit int, before string) any
	Restore    func(expected int64, entryID, scope string) (policy.State, error)
	Version    string
	HostSchema uint32
	Guard      bool
	// AliasStatus optionally supplies cached runtime status without evaluating aliases.
	// If absent, read routes use Check with dryRun=true.
	AliasStatus func() any
	// Revision-aware callbacks take precedence over their compatibility counterparts.
	// They must compare expected under the same owner lock used for the operation.
	CheckRevision   func(context.Context, int64, string, bool) (any, error)
	RefreshRevision func(context.Context, int64) (policy.Catalog, error)
}

type Handler struct {
	config Config
	mu     sync.Mutex
}

func New(config Config) *Handler { return &Handler{config: config} }

type route struct {
	Method string
	Path   string
}

var routes = []route{
	{"GET", "/overview"}, {"GET", "/state"}, {"PUT", "/state"},
	{"POST", "/validate"}, {"GET", "/catalog"}, {"POST", "/catalog/refresh"},
	{"POST", "/aliases/check"}, {"POST", "/rules/preview"}, {"POST", "/explain"},
	{"GET", "/history"}, {"POST", "/history/restore"},
}

// Register returns the native RPC shape, which differs from the SDK's untagged
// ManagementRegistrationResponse. Inner route fields retain the official SDK names.
func (h *Handler) Register(req pluginapi.ManagementRegistrationRequest) any {
	base := strings.TrimRight(req.BasePath, "/")
	if base == "" {
		base = "/v0/management"
	}
	base += "/plugins/cpa-model-dock"
	resources := strings.TrimRight(req.ResourceBasePath, "/")
	if resources == "" {
		resources = ResourceBasePath
	}
	type resource struct {
		Path        string
		Menu        string `json:",omitempty"`
		Description string `json:",omitempty"`
	}
	registration := struct {
		Routes    []route    `json:"routes"`
		Resources []resource `json:"resources"`
	}{Routes: make([]route, 0, len(routes)), Resources: make([]resource, 0, 4)}
	for _, r := range routes {
		registration.Routes = append(registration.Routes, route{r.Method, base + r.Path})
	}
	for _, name := range []string{"index.html", "app.js", "app.css", "i18n.js"} {
		r := resource{Path: resources + "/" + name}
		if name == "index.html" {
			r.Menu, r.Description = "Model Dock", "Manage model aliases and whitelist"
		}
		registration.Resources = append(registration.Resources, r)
	}
	return registration
}

func (h *Handler) snapshot() *policy.Snapshot {
	if h.config.Current != nil {
		if s := h.config.Current(); s != nil {
			return s
		}
	}
	return policy.New(policy.DefaultState(), policy.Catalog{}, false)
}

// Handle dispatches only exact contract paths. Public resources never touch callbacks.
func (h *Handler) Handle(ctx context.Context, req pluginapi.ManagementRequest) (resp pluginapi.ManagementResponse) {
	defer func() {
		if recover() != nil {
			resp = failure(http.StatusInternalServerError, "internal_error", "Management operation failed")
		}
	}()
	if strings.HasPrefix(req.Path, ResourceBasePath+"/") {
		return resourceResponse(req)
	}
	path := strings.TrimPrefix(req.Path, BasePath)
	if path == req.Path {
		return failure(http.StatusNotFound, "not_found", "Unknown route")
	}
	known, allowed := false, ""
	for _, r := range routes {
		if path == r.Path {
			known = true
			if allowed != "" {
				allowed += ", "
			}
			allowed += r.Method
			if req.Method == r.Method {
				return h.dispatch(ctx, path, req)
			}
		}
	}
	if known {
		resp = failure(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed")
		resp.Headers.Set("Allow", allowed)
		return resp
	}
	return failure(http.StatusNotFound, "not_found", "Unknown route")
}

func (h *Handler) dispatch(ctx context.Context, path string, req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	if req.Method == http.MethodGet {
		s := h.snapshot()
		switch path {
		case "/state":
			return response(http.StatusOK, h.stateView(ctx, s))
		case "/overview":
			return response(http.StatusOK, h.overview(ctx, s))
		case "/catalog":
			return response(http.StatusOK, catalogView(s, s.CatalogCopy()))
		case "/history":
			return h.history(req)
		}
	}
	body, details := decodeObject(req.Body)
	if len(details) != 0 {
		return invalid(details)
	}
	switch path {
	case "/validate":
		return h.validate(body)
	case "/rules/preview":
		return h.preview(body)
	case "/explain":
		return h.explain(body)
	}
	// Revision checks and callbacks form one critical section for all management writes.
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.snapshot()
	if path == "/aliases/check" {
		var dryRun bool
		if d := field(body, "dry_run", &dryRun); len(d) != 0 {
			return invalid(d)
		}
		if dryRun {
			return h.check(ctx, body, true, s)
		}
	}
	expected, details := revision(req.Headers, body)
	if len(details) != 0 {
		return invalid(details)
	}
	if expected != s.StateCopy().Revision {
		return h.conflict()
	}
	switch path {
	case "/state":
		state, details := fullState(body, s.StateCopy())
		if len(details) != 0 {
			return invalid(details)
		}
		if details = policy.Validate(state, s.CatalogCopy()); len(details) != 0 {
			return invalid(details)
		}
		if h.config.Save == nil {
			return unavailable()
		}
		saved, err := h.config.Save(expected, state, "user", "state", "Updated model policy")
		return h.saved(saved, err)
	case "/catalog/refresh":
		var catalog policy.Catalog
		if h.config.RefreshRevision != nil {
			var err error
			catalog, err = h.config.RefreshRevision(ctx, expected)
			if err != nil {
				return h.callbackError(err)
			}
		} else if h.config.Refresh != nil {
			catalog = h.config.Refresh(ctx)
		} else {
			return unavailable()
		}
		return response(http.StatusOK, catalogView(h.snapshot(), catalog))
	case "/aliases/check":
		return h.check(ctx, body, false, s)
	case "/history/restore":
		var entryID, scope string
		details = append(field(body, "entry_id", &entryID), field(body, "scope", &scope)...)
		if strings.TrimSpace(entryID) == "" {
			details = append(details, detail("entry_id", "required", "A history entry is required"))
		}
		if scope != "state" && scope != "alias_target" {
			details = append(details, detail("scope", "invalid_scope", "Scope must be state or alias_target"))
		}
		if len(details) != 0 {
			return invalid(details)
		}
		if h.config.Restore == nil {
			return unavailable()
		}
		saved, err := h.config.Restore(expected, entryID, scope)
		return h.saved(saved, err)
	}
	return failure(http.StatusNotFound, "not_found", "Unknown route")
}

func (h *Handler) saved(state policy.State, err error) pluginapi.ManagementResponse {
	if err != nil {
		return h.callbackError(err)
	}
	return response(http.StatusOK, state)
}

func (h *Handler) callbackError(err error) pluginapi.ManagementResponse {
	var validation interface{ ValidationDetails() []policy.Detail }
	if errors.As(err, &validation) {
		return invalid(validation.ValidationDetails())
	}
	switch {
	case errors.Is(err, store.ErrRevisionConflict):
		return h.conflict()
	case errors.Is(err, store.ErrEntryNotFound):
		return failure(http.StatusNotFound, "entry_not_found", "History entry not found")
	case errors.Is(err, store.ErrInvalidScope):
		return invalid([]policy.Detail{detail("scope", "invalid_scope", "Scope cannot be restored from this entry")})
	case errors.Is(err, store.ErrUnavailable):
		return unavailable()
	case errors.Is(err, store.ErrInvalidState):
		return invalid([]policy.Detail{detail("state", "invalid_state", "State could not be saved")})
	default:
		return failure(http.StatusInternalServerError, "operation_failed", "Management operation failed")
	}
}

func (h *Handler) conflict() pluginapi.ManagementResponse {
	return response(http.StatusConflict, map[string]any{
		"error":   map[string]any{"code": "revision_conflict", "message": "Policy revision has changed"},
		"current": h.snapshot().StateCopy(),
	})
}

func resourceResponse(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	headers := http.Header{
		"Cache-Control":           {"no-cache"},
		"X-Content-Type-Options":  {"nosniff"},
		"Content-Security-Policy": {"default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'"},
	}
	name := strings.TrimPrefix(req.Path, ResourceBasePath)
	contentType, exists := assets[name]
	if !exists {
		headers.Set("Content-Type", "text/plain; charset=utf-8")
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: headers}
	}
	headers.Set("Content-Type", contentType)
	if req.Method != http.MethodGet {
		headers.Set("Allow", "GET")
		return pluginapi.ManagementResponse{StatusCode: http.StatusMethodNotAllowed, Headers: headers}
	}
	body, err := web.Assets.ReadFile("assets" + name)
	if err != nil {
		return pluginapi.ManagementResponse{StatusCode: http.StatusNotFound, Headers: headers}
	}
	return pluginapi.ManagementResponse{StatusCode: http.StatusOK, Headers: headers, Body: body}
}

func response(status int, value any) pluginapi.ManagementResponse {
	body, err := json.Marshal(value)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":{"code":"encoding_failed","message":"Management response unavailable"}}`)
	}
	return pluginapi.ManagementResponse{StatusCode: status, Body: body, Headers: http.Header{
		"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-cache"}, "X-Content-Type-Options": {"nosniff"},
	}}
}

func failure(status int, code, message string) pluginapi.ManagementResponse {
	return response(status, map[string]any{"error": map[string]any{"code": code, "message": message}})
}

func unavailable() pluginapi.ManagementResponse {
	return failure(http.StatusServiceUnavailable, "policy_unavailable", "Management dependency unavailable")
}

func detail(path, code, message string) policy.Detail {
	return policy.Detail{Path: path, Code: code, Message: message}
}

func invalid(details []policy.Detail) pluginapi.ManagementResponse {
	if details == nil {
		details = []policy.Detail{}
	}
	return response(http.StatusUnprocessableEntity, map[string]any{"error": map[string]any{
		"code": "validation_failed", "details": details,
	}})
}
