package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cpa-model-dock/cpa-model-dock/internal/autoupdate"
	"github.com/cpa-model-dock/cpa-model-dock/internal/catalog"
	"github.com/cpa-model-dock/cpa-model-dock/internal/hooks"
	"github.com/cpa-model-dock/cpa-model-dock/internal/hostabi"
	"github.com/cpa-model-dock/cpa-model-dock/internal/mgmt"
	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/cpa-model-dock/cpa-model-dock/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

const pluginID = "cpa-model-dock"

var pluginVersion = "0.1.0"

const healthProbeTimeout = 5 * time.Second

// Process-wide, so a native callback ignoring cancellation cannot leak a new
// paid probe worker on every timeout, restart, or state-directory replacement.
var healthProbes hostabi.CallGate

type config struct {
	StateDir        string `yaml:"state-dir"`
	RefreshInterval string `yaml:"catalog-refresh-interval"`
	Guard           *bool  `yaml:"guard-uncovered-endpoints"`
	Priority        int    `yaml:"priority"`
}

func parseConfig(raw []byte) (config, time.Duration, error) {
	var c config
	if yaml.Unmarshal(raw, &c) != nil {
		return c, 0, errors.New("invalid plugin configuration")
	}
	if c.StateDir == "" {
		home, e := os.UserHomeDir()
		if e != nil {
			return c, 0, errors.New("cannot determine state directory")
		}
		c.StateDir = filepath.Join(home, ".cli-proxy-api", "cpa-model-dock")
	}
	c.StateDir = filepath.Clean(c.StateDir)
	if !filepath.IsAbs(c.StateDir) {
		return c, 0, errors.New("state-dir must be absolute")
	}
	interval := 5 * time.Minute
	if c.RefreshInterval != "" {
		var e error
		interval, e = time.ParseDuration(c.RefreshInterval)
		if e != nil || interval < 30*time.Second {
			return c, 0, errors.New("catalog-refresh-interval must be at least 30s")
		}
	}
	return c, interval, nil
}
func (c config) guard() bool { return c.Guard == nil || *c.Guard }

type application struct {
	mu           sync.Mutex // serializes state saves and catalog/auto publication
	current      *atomic.Pointer[policy.Snapshot]
	store        *store.Store
	catalog      *catalog.Service
	hooks        *hooks.Hooks
	host         hostabi.Caller
	status       map[string]autoupdate.Status
	workCtx      context.Context
	cancel       context.CancelFunc
	baseInterval time.Duration
}

func newApplication(dir string, host hostabi.Caller) (*application, error) {
	disk, e := store.Open(dir)
	if e != nil {
		return nil, e
	}
	state, valid, e := disk.Load()
	if e != nil && !errors.Is(e, store.ErrUnavailable) {
		return nil, e
	}
	workCtx, cancel := context.WithCancel(context.Background())
	a := &application{workCtx: workCtx, cancel: cancel, current: &atomic.Pointer[policy.Snapshot]{}, store: disk, catalog: catalog.New(host), host: host, status: map[string]autoupdate.Status{}}
	a.current.Store(policy.New(state, policy.Catalog{Models: []policy.Model{}}, valid))
	a.hooks = hooks.New(a.current.Load)
	return a, nil
}
func (a *application) publish(next *policy.Snapshot) { a.hooks.Update(next); a.current.Store(next) }
func (a *application) save(expected int64, state policy.State, actor, kind, summary string) (policy.State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workCtx.Err() != nil {
		return policy.State{}, store.ErrUnavailable
	}
	snap := a.current.Load()
	if expected != snap.StateCopy().Revision {
		return policy.State{}, store.ErrRevisionConflict
	}
	if details := mgmt.ValidateState(state, snap.CatalogCopy()); len(details) > 0 {
		return policy.State{}, validationFailure{details}
	}
	saved, e := a.store.Save(expected, state, actor, kind, summary)
	if e != nil {
		return policy.State{}, e
	}
	a.publish(policy.New(saved, snap.CatalogCopy(), true))
	a.setInterval(saved.Settings)
	a.status = map[string]autoupdate.Status{}
	return saved, nil
}
func (a *application) restore(expected int64, id, scope string) (policy.State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.workCtx.Err() != nil {
		return policy.State{}, store.ErrUnavailable
	}
	snap := a.current.Load()
	if expected != snap.StateCopy().Revision {
		return policy.State{}, store.ErrRevisionConflict
	}
	// Validate the prospective restored state before the store performs any write.
	var found *store.Entry
	entryID, aliasID, _ := strings.Cut(id, ":")
	for _, entry := range a.store.History(1000, "") {
		if entry.ID == entryID {
			v := entry
			found = &v
			break
		}
	}
	if found == nil || (found.Kind == "state" && aliasID != "") || (found.Kind == "alias_target" && aliasID == "") {
		return policy.State{}, store.ErrEntryNotFound
	}
	if scope != found.Kind {
		return policy.State{}, store.ErrInvalidScope
	}
	draft := found.Before
	if scope == "alias_target" {
		draft = snap.StateCopy()
		oldTargets := map[string]string{}
		for _, alias := range found.Before.Aliases {
			oldTargets[alias.ID] = alias.Target
		}
		matched := false
		for _, alias := range found.After.Aliases {
			old, exists := oldTargets[alias.ID]
			if !exists || old == alias.Target || (aliasID != "" && aliasID != alias.ID) {
				continue
			}
			present := false
			for i := range draft.Aliases {
				if draft.Aliases[i].ID == alias.ID {
					draft.Aliases[i].Target = old
					present = true
					matched = true
				}
			}
			if !present {
				return policy.State{}, validationFailure{[]policy.Detail{policy.NewDetail("entry_id", "alias_not_found", "Historical alias no longer exists")}}
			}
		}
		if !matched {
			return policy.State{}, store.ErrEntryNotFound
		}
	} else if scope != "state" {
		return policy.State{}, store.ErrInvalidScope
	}
	if details := mgmt.ValidateState(draft, snap.CatalogCopy()); len(details) > 0 {
		return policy.State{}, validationFailure{details}
	}
	restored, e := a.store.Save(expected, draft, "user", scope, "Restored before history entry "+id)
	if e != nil {
		return policy.State{}, e
	}
	a.publish(policy.New(restored, snap.CatalogCopy(), true))
	a.setInterval(restored.Settings)
	if scope == "state" {
		a.status = map[string]autoupdate.Status{}
	} else {
		delete(a.status, aliasID)
	}
	return restored, nil
}

// workContext pins this lifecycle without holding a mutation lock during I/O.
func (a *application) workContext() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.workCtx
}

func linkedContext(ctx, lifecycle context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(lifecycle, cancel)
	if lifecycle.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (a *application) refresh(ctx context.Context) policy.Catalog {
	life := a.workContext()
	ctx, cancel := linkedContext(ctx, life)
	defer cancel()
	c := a.catalog.Refresh(ctx)
	a.publishCatalog(life, c)
	return a.current.Load().CatalogCopy()
}

func (a *application) publishCatalog(life context.Context, c policy.Catalog) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if life != a.workCtx || life.Err() != nil {
		return false
	}
	snap := a.current.Load()
	if c.Generation <= snap.CatalogCopy().Generation {
		return false
	}
	return a.catalog.WithCurrentGeneration(c.Generation, func() {
		a.publish(policy.New(snap.StateCopy(), c, snap.Available()))
	})
}

func (a *application) onCatalog(c policy.Catalog) { a.onCatalogContext(a.workContext(), c) }
func (a *application) onCatalogContext(life context.Context, c policy.Catalog) {
	if !a.publishCatalog(life, c) {
		return
	}
	// Bind evaluation to this delivery, not whatever a later refresh published.
	// Conflicts/cancellation leave the last policy intact; the next poll retries.
	_, _ = a.evaluate(life, nil, &c.Generation, "", false)
}

func (a *application) check(ctx context.Context, id string, dryRun bool) (any, error) {
	return a.evaluate(ctx, nil, nil, id, dryRun)
}

// Capture immutable inputs, perform paid work without a.mu, then compare both
// policy revision and catalog generation before touching persistence or status.
func (a *application) evaluate(ctx context.Context, expected *int64, generation *uint64, id string, dryRun bool) (any, error) {
	a.mu.Lock()
	snap, life := a.current.Load(), a.workCtx
	state := snap.StateCopy()
	if life.Err() != nil {
		a.mu.Unlock()
		return nil, context.Canceled
	}
	if !dryRun && ((expected != nil && *expected != state.Revision) || (generation != nil && *generation != snap.CatalogCopy().Generation)) {
		a.mu.Unlock()
		return nil, store.ErrRevisionConflict
	}
	if id != "" {
		found := false
		for _, alias := range state.Aliases {
			if alias.ID == id {
				found = true
				break
			}
		}
		if !found {
			a.mu.Unlock()
			return nil, store.ErrEntryNotFound
		}
	}
	a.mu.Unlock()
	ctx, cancel := linkedContext(ctx, life)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Reject a completed refresh even when its delivery is still queued on a.mu.
	if !dryRun && !a.catalog.WithCurrentGeneration(snap.CatalogCopy().Generation, func() {}) {
		return nil, store.ErrRevisionConflict
	}
	var probe autoupdate.Probe
	if !dryRun {
		probe = a.probe
	}
	var evaluated policy.State
	var status map[string]autoupdate.Status
	if id != "" {
		evaluated, status = autoupdate.EvaluateAlias(ctx, snap, probe, id)
	} else {
		evaluated, status = autoupdate.Evaluate(ctx, snap, probe)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dryRun {
		return status, nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current := a.current.Load()
	if life != a.workCtx || life.Err() != nil || ctx.Err() != nil {
		return nil, context.Canceled
	}
	if current.StateCopy().Revision != state.Revision || current.CatalogCopy().Generation != snap.CatalogCopy().Generation {
		return nil, store.ErrRevisionConflict
	}
	var commitErr error
	fresh := a.catalog.WithCurrentGeneration(snap.CatalogCopy().Generation, func() {
		changed := false
		for i := range state.Aliases {
			for _, updated := range evaluated.Aliases {
				if state.Aliases[i].ID == updated.ID && state.Aliases[i].Target != updated.Target {
					state.Aliases[i].Target = updated.Target
					changed = true
				}
			}
		}
		if changed {
			saved, err := a.store.Save(state.Revision, state, "auto", "alias_target", "Updated aliases to newer routable family targets")
			if err != nil {
				commitErr = err
				return
			}
			a.publish(policy.New(saved, snap.CatalogCopy(), true))
		}
		if id == "" {
			a.status = status
		} else {
			for k, v := range status {
				a.status[k] = v
			}
		}
	})
	if !fresh {
		return nil, store.ErrRevisionConflict
	}
	if commitErr != nil {
		return nil, commitErr
	}
	return status, nil
}
func (a *application) probe(ctx context.Context, alias policy.Alias, model policy.Model) error {
	if a.host == nil {
		return errors.New("host callback unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, healthProbeTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Hi"}}, "max_tokens": 1, "stream": false})
	var out pluginapi.HostModelExecutionResponse
	err := healthProbes.Call(ctx, a.host, pluginabi.MethodHostModelExecute, pluginapi.HostModelExecutionRequest{EntryProtocol: "openai", Model: model.ID, ForcedProvider: alias.Provider, Body: body}, &out)
	if err != nil || out.StatusCode < 200 || out.StatusCode >= 300 {
		return errors.New("health check failed")
	}
	return nil
}
func (a *application) setInterval(settings policy.Settings) {
	interval := a.baseInterval
	if settings.RefreshIntervalSeconds > 0 {
		interval = time.Duration(settings.RefreshIntervalSeconds) * time.Second
	}
	a.catalog.SetInterval(interval)
}
func (a *application) start(interval time.Duration) {
	a.stop()
	a.mu.Lock()
	a.baseInterval = interval
	if seconds := a.current.Load().StateCopy().Settings.RefreshIntervalSeconds; seconds > 0 {
		interval = time.Duration(seconds) * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.workCtx, a.cancel = ctx, cancel
	a.mu.Unlock()
	a.catalog.Start(ctx, interval, func(c policy.Catalog) { a.onCatalogContext(ctx, c) })
}

// Cancel before acquiring the runtime write lock: management Dispatch holds its
// read lock while awaiting checks, so cancellation must be reachable by writers.
func (a *application) cancelWork() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancel()
}
func (a *application) stop() {
	a.cancelWork()
	a.catalog.Stop()
}
func (a *application) close() { a.stop(); a.hooks.Close() }
func (a *application) management(schema uint32, guard bool) *mgmt.Handler {
	return mgmt.New(mgmt.Config{Current: a.current.Load, Save: a.save, Refresh: a.refresh, Check: a.check,
		History: a.managementHistory, Restore: a.restore,
		Version: pluginVersion, HostSchema: schema, Guard: guard, CheckRevision: a.checkRevision, RefreshRevision: a.refreshRevision,
		AliasStatus: func() any {
			a.mu.Lock()
			defer a.mu.Unlock()
			out := map[string]autoupdate.Status{}
			for k, v := range a.status {
				out[k] = v
			}
			return out
		},
	})
}

func (a *application) checkRevision(ctx context.Context, expected int64, id string, dryRun bool) (any, error) {
	return a.evaluate(ctx, &expected, nil, id, dryRun)
}

// Catalog refresh is independent of policy optimistic concurrency.
func (a *application) refreshRevision(ctx context.Context, _ int64) (policy.Catalog, error) {
	return a.refresh(ctx), nil
}

// Disk history keeps full snapshots; management exposes one target pair per alias.
func (a *application) managementHistory(limit int, before string) any {
	rows := []any{}
	for _, entry := range a.store.History(store.HistoryLimit, "") {
		row := func(id string, old, next any, detail policy.Text) any {
			return map[string]any{"id": id, "at": entry.At, "actor": entry.Actor, "kind": entry.Kind, "summary": detail.Message, "summary_detail": detail, "before": old, "after": next}
		}
		if entry.Kind == "state" {
			rows = append(rows, row(entry.ID, entry.Before, entry.After, policy.HistorySummary(entry.Before, entry.After, entry.Actor, entry.Summary)))
			continue
		}
		old := map[string]policy.Alias{}
		for _, alias := range entry.Before.Aliases {
			old[alias.ID] = alias
		}
		aliases := append([]policy.Alias{}, entry.After.Aliases...)
		sort.Slice(aliases, func(i, j int) bool { return aliases[i].ID < aliases[j].ID })
		for _, alias := range aliases {
			if previous, exists := old[alias.ID]; exists && previous.Target != alias.Target {
				detail := policy.HistorySummary(policy.State{Aliases: []policy.Alias{previous}}, policy.State{Aliases: []policy.Alias{alias}}, entry.Actor, entry.Summary)
				rows = append(rows, row(entry.ID+":"+alias.ID, map[string]any{"alias_id": alias.ID, "target": previous.Target}, map[string]any{"alias_id": alias.ID, "target": alias.Target}, detail))
			}
		}
	}
	if len(rows) > store.HistoryLimit {
		rows = rows[:store.HistoryLimit]
	}
	return rows
}

// Validation failures carry structured fields through the management callback.
type validationFailure struct{ details []policy.Detail }

func (e validationFailure) Error() string                      { return "state validation failed" }
func (e validationFailure) ValidationDetails() []policy.Detail { return e.details }
