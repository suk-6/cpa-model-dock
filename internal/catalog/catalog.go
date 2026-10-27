// Package catalog caches sanitized host registration facts. Refresh is never
// performed on an inference request path.
//
// CPA v8.0.44 HostAuthModels exposes registered public IDs, not upstream mapping
// provenance. HostAuthFileEntry exposes neither Prefix nor credential alias
// mappings. Consequently a slash in a registered ID establishes no trusted
// prefix, and a different resolved destination establishes no trusted CPA alias.
// Missing runtime entries are not treated as enabled credentials. The callbacks
// cannot expose static executor-only models or guarantee request-time eligibility.
package catalog

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cpa-model-dock/cpa-model-dock/internal/hostabi"
	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

const MinInterval = 30 * time.Second
const DefaultInterval = 5 * time.Minute

type Service struct {
	caller          hostabi.Caller
	snapshot        atomic.Pointer[policy.Catalog]
	refreshMu       sync.Mutex
	lifecycleMu     sync.Mutex
	cancel          context.CancelFunc
	done            chan struct{}
	interval        atomic.Int64
	intervalChanged chan struct{}
}

// These projections match the real HostAuthModels{Request,Response} and
// HostAuthFileEntry/get_runtime wire DTOs in sdk/pluginapi/types.go. Unneeded
// email, path, base_url, account and status_message fields are never decoded.
type modelsResponse struct {
	Credentials []credentialModels `json:"credentials"`
}
type credentialModels struct {
	ID        string   `json:"id"`
	AuthIndex string   `json:"auth_index"`
	Provider  string   `json:"provider"`
	Models    []string `json:"models"`
}
type runtimeAuth struct {
	ID          string `json:"id"`
	AuthIndex   string `json:"auth_index"`
	Disabled    bool   `json:"disabled"`
	Unavailable bool   `json:"unavailable"`
	Status      string `json:"status"`
	// Not sent by stock CPA v8.0.44. Accept only explicit runtime evidence
	// from a future host; never derive this from model-name path segments.
	Prefix string `json:"prefix"`
}
type listResponse struct {
	Files []runtimeAuth `json:"files"`
}
type runtimeResponse struct {
	Auth runtimeAuth `json:"auth"`
}

func New(caller hostabi.Caller) *Service {
	s := &Service{caller: caller, intervalChanged: make(chan struct{}, 1)}
	s.interval.Store(int64(DefaultInterval))
	initial := policy.Catalog{Models: []policy.Model{}, Prefixes: []string{}}
	s.snapshot.Store(&initial)
	return s
}

func (s *Service) Snapshot() policy.Catalog { return copyCatalog(*s.snapshot.Load()) }

// Refresh publishes only after every requested callback succeeds. Error text is
// fixed and sanitized: host errors can contain credential identifiers/secrets.
func (s *Service) Refresh(ctx context.Context) (result policy.Catalog) {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	defer func() {
		if recover() != nil {
			result = s.failed("host catalog callback failed")
		}
	}()
	if s.caller == nil {
		return s.failed("host catalog callback unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var registrations modelsResponse
	if err := s.caller.Call(ctx, pluginabi.MethodHostAuthModels, struct{}{}, &registrations); err != nil {
		return s.failed("host model inventory unavailable")
	}
	var listed listResponse
	if err := s.caller.Call(ctx, pluginabi.MethodHostAuthList, struct{}{}, &listed); err != nil {
		return s.failed("host credential inventory unavailable")
	}
	runtimeByIndex := make(map[string]runtimeAuth)
	runtimeByID := make(map[string]runtimeAuth)
	prefixes := make(map[string]struct{})
	for _, auth := range listed.Files {
		if auth.AuthIndex != "" {
			var current runtimeResponse
			if err := s.caller.Call(ctx, pluginabi.MethodHostAuthGetRuntime, struct {
				AuthIndex string `json:"auth_index"`
			}{auth.AuthIndex}, &current); err != nil {
				return s.failed("host credential runtime unavailable")
			}
			// A null/missing auth must not become an implicitly enabled record.
			if current.Auth.AuthIndex != auth.AuthIndex {
				return s.failed("host credential runtime invalid")
			}
			auth = current.Auth
		}
		if auth.AuthIndex != "" {
			runtimeByIndex[auth.AuthIndex] = auth
		}
		if auth.ID != "" {
			runtimeByID[auth.ID] = auth
		}
		if prefix := strings.TrimSpace(auth.Prefix); prefix != "" {
			prefixes[prefix] = struct{}{}
		}
	}
	type aggregate struct {
		model       policy.Model
		providers   map[string]struct{}
		serving     map[string]struct{}
		credentials map[string]struct{}
	}
	models := make(map[string]*aggregate)
	for _, credential := range registrations.Credentials {
		auth, confirmed := runtimeByIndex[credential.AuthIndex]
		if !confirmed && credential.ID != "" {
			auth, confirmed = runtimeByID[credential.ID]
		}
		enabled := confirmed && !auth.Disabled && !auth.Unavailable && !strings.EqualFold(strings.TrimSpace(auth.Status), "disabled")
		key := credential.ID
		if key == "" {
			key = credential.AuthIndex
		}
		if key == "" {
			enabled = false
		}
		for _, id := range credential.Models {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			entry := models[id]
			if entry == nil {
				entry = &aggregate{model: policy.Model{ID: id}, providers: make(map[string]struct{}), serving: make(map[string]struct{}), credentials: make(map[string]struct{})}
				models[id] = entry
			}
			if provider := strings.TrimSpace(credential.Provider); provider != "" {
				entry.providers[provider] = struct{}{}
			}
			if enabled {
				entry.credentials[key] = struct{}{}
				if provider := strings.TrimSpace(credential.Provider); provider != "" {
					entry.serving[provider] = struct{}{}
				}
			}
		}
	}
	if ctx.Err() != nil {
		return s.failed("host catalog refresh cancelled")
	}
	next := policy.Catalog{Models: make([]policy.Model, 0, len(models)), Prefixes: make([]string, 0, len(prefixes)), RefreshedAt: time.Now().UTC(), OK: true}
	for _, entry := range models {
		// For routable models, only advertise providers with enabled runtime
		// evidence. An unavailable provider must not inherit another provider's
		// routability merely because both advertise the same model ID.
		providers := entry.providers
		if len(entry.credentials) > 0 {
			providers = entry.serving
		}
		entry.model.Providers = make([]string, 0, len(providers))
		for provider := range providers {
			entry.model.Providers = append(entry.model.Providers, provider)
		}
		sort.Strings(entry.model.Providers)
		entry.model.CredentialCount = len(entry.credentials)
		entry.model.Routable = entry.model.CredentialCount > 0
		next.Models = append(next.Models, entry.model)
	}
	for prefix := range prefixes {
		next.Prefixes = append(next.Prefixes, prefix)
	}
	sort.Strings(next.Prefixes)
	sort.Slice(next.Models, func(i, j int) bool { return next.Models[i].ID < next.Models[j].ID })
	s.snapshot.Store(&next)
	return copyCatalog(next)
}

func (s *Service) failed(message string) policy.Catalog {
	next := copyCatalog(*s.snapshot.Load())
	next.OK, next.Error = false, message
	// RefreshedAt records the attempted refresh; last-good model facts survive.
	next.RefreshedAt = time.Now().UTC()
	s.snapshot.Store(&next)
	return copyCatalog(next)
}

// Start replaces any existing poller and refreshes immediately. Cancellation or
// Stop drains the current poller. onRefresh runs serially and must not call Start
// or Stop synchronously; it may call Snapshot/Refresh safely.
func (s *Service) Start(ctx context.Context, interval time.Duration, onRefresh func(policy.Catalog)) {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stop()
	s.SetInterval(interval)
	ctx, s.cancel = context.WithCancel(ctx)
	s.done = make(chan struct{})
	done := s.done
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Duration(s.interval.Load()))
		defer ticker.Stop()
		refresh := func() {
			catalog := s.Refresh(ctx)
			if ctx.Err() == nil && onRefresh != nil {
				notify(onRefresh, catalog)
			}
		}
		if ctx.Err() != nil {
			return
		}
		refresh()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.intervalChanged:
				ticker.Reset(time.Duration(s.interval.Load()))
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				refresh()
			}
		}
	}()
}

// SetInterval updates a running poller without waiting for refresh callbacks.
// It can be called under the management app mutex: it never acquires a lifecycle
// lock or invokes onRefresh. Multiple updates coalesce to the latest value.
func (s *Service) SetInterval(interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	if interval < MinInterval {
		interval = MinInterval
	}
	s.interval.Store(int64(interval))
	select {
	case s.intervalChanged <- struct{}{}:
	default:
	}
}

func notify(fn func(policy.Catalog), catalog policy.Catalog) {
	defer func() { _ = recover() }()
	fn(catalog)
}

func (s *Service) Stop() {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stop()
}

func (s *Service) stop() {
	if s.cancel != nil {
		s.cancel()
		<-s.done
		s.cancel, s.done = nil, nil
	}
}

func copyCatalog(catalog policy.Catalog) policy.Catalog {
	catalog.Models = append([]policy.Model{}, catalog.Models...)
	for i := range catalog.Models {
		catalog.Models[i].Providers = append([]string{}, catalog.Models[i].Providers...)
	}
	catalog.Prefixes = append([]string{}, catalog.Prefixes...)
	return catalog
}
