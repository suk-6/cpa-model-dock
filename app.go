package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	a := &application{current: &atomic.Pointer[policy.Snapshot]{}, store: disk, catalog: catalog.New(host), host: host, status: map[string]autoupdate.Status{}}
	a.current.Store(policy.New(state, policy.Catalog{Models: []policy.Model{}}, valid))
	a.hooks = hooks.New(a.current.Load)
	return a, nil
}
func (a *application) publish(next *policy.Snapshot) { a.hooks.Update(next); a.current.Store(next) }
func (a *application) save(expected int64, state policy.State, actor, kind, summary string) (policy.State, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
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
				return policy.State{}, validationFailure{[]policy.Detail{{Path: "entry_id", Code: "alias_not_found", Message: "Historical alias no longer exists"}}}
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
func (a *application) refresh(ctx context.Context) policy.Catalog {
	c := a.catalog.Refresh(ctx)
	a.mu.Lock()
	snap := a.current.Load()
	a.publish(policy.New(snap.StateCopy(), c, snap.Available()))
	a.mu.Unlock()
	return c
}
func (a *application) onCatalog(c policy.Catalog) {
	a.mu.Lock()
	snap := a.current.Load()
	a.publish(policy.New(snap.StateCopy(), c, snap.Available()))
	a.mu.Unlock()
	if _, e := a.check(context.Background(), "", false); e != nil {
		hostabi.Log(a.host, "warn", "CPAModelDock automatic evaluation could not be saved")
	}
}
func (a *application) check(ctx context.Context, id string, dryRun bool) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.checkLocked(ctx, id, dryRun)
}
func (a *application) checkLocked(ctx context.Context, id string, dryRun bool) (any, error) {
	snap := a.current.Load()
	state := snap.StateCopy()
	var probe autoupdate.Probe
	if !dryRun {
		probe = a.probe
	}
	var evaluated policy.State
	var status map[string]autoupdate.Status
	if id != "" {
		found := false
		for _, alias := range state.Aliases {
			if alias.ID == id {
				found = true
				break
			}
		}
		if !found {
			return nil, store.ErrEntryNotFound
		}
		evaluated, status = autoupdate.EvaluateAlias(ctx, snap, probe, id)
	} else {
		evaluated, status = autoupdate.Evaluate(ctx, snap, probe)
	}
	if dryRun {
		return status, nil
	}
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
		saved, e := a.store.Save(state.Revision, state, "auto", "alias_target", "Updated aliases to newer routable family targets")
		if e != nil {
			return nil, e
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
	return status, nil
}
func (a *application) probe(ctx context.Context, alias policy.Alias, model policy.Model) error {
	if a.host == nil {
		return errors.New("host callback unavailable")
	}
	body, _ := json.Marshal(map[string]any{"model": model.ID, "messages": []map[string]string{{"role": "user", "content": "Hi"}}, "max_tokens": 1, "stream": false})
	var out pluginapi.HostModelExecutionResponse
	err := a.host.Call(ctx, pluginabi.MethodHostModelExecute, pluginapi.HostModelExecutionRequest{EntryProtocol: "openai", Model: model.ID, ForcedProvider: alias.Provider, Body: body}, &out)
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
	a.baseInterval = interval
	if seconds := a.current.Load().StateCopy().Settings.RefreshIntervalSeconds; seconds > 0 {
		interval = time.Duration(seconds) * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.cancel = cancel
	a.catalog.Start(ctx, interval, a.onCatalog)
}
func (a *application) stop() {
	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}
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
	a.mu.Lock()
	defer a.mu.Unlock()
	if !dryRun && expected != a.current.Load().StateCopy().Revision {
		return nil, store.ErrRevisionConflict
	}
	return a.checkLocked(ctx, id, dryRun)
}

// Catalog refresh is independent of policy optimistic concurrency.
func (a *application) refreshRevision(ctx context.Context, _ int64) (policy.Catalog, error) {
	return a.refresh(ctx), nil
}

// Disk history keeps full snapshots; management exposes one target pair per alias.
func (a *application) managementHistory(limit int, before string) any {
	rows := []any{}
	for _, entry := range a.store.History(store.HistoryLimit, "") {
		row := func(id string, old, next any) any {
			return map[string]any{"id": id, "at": entry.At, "actor": entry.Actor, "kind": entry.Kind, "summary": entry.Summary, "before": old, "after": next}
		}
		if entry.Kind == "state" {
			rows = append(rows, row(entry.ID, entry.Before, entry.After))
			continue
		}
		old := map[string]string{}
		for _, alias := range entry.Before.Aliases {
			old[alias.ID] = alias.Target
		}
		for _, alias := range entry.After.Aliases {
			if target, exists := old[alias.ID]; exists && target != alias.Target {
				rows = append(rows, row(entry.ID+":"+alias.ID, map[string]any{"alias_id": alias.ID, "target": target}, map[string]any{"alias_id": alias.ID, "target": alias.Target}))
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
