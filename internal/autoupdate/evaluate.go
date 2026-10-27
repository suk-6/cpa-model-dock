// Package autoupdate evaluates immutable policy/catalog snapshots. The caller
// owns persistence, revision checks, scheduling, and dry-run behavior.
package autoupdate

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/suk-6/cpa-model-dock/internal/policy"
	"github.com/suk-6/cpa-model-dock/internal/version"
)

// Probe is an optional paid health check. Errors and panics preserve the old
// target; raw upstream errors are deliberately not exposed in status messages.
type Probe func(context.Context, policy.Alias, policy.Model) error

type Candidate struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	Date       string `json:"date,omitempty"`
	Prerelease string `json:"prerelease,omitempty"`
	Routable   bool   `json:"routable"`
	Chosen     bool   `json:"chosen"`
	Reason     string `json:"reason"`
}

type Status struct {
	CurrentTarget  string      `json:"current_target"`
	PreviousTarget string      `json:"previous_target,omitempty"`
	Candidates     []Candidate `json:"candidates"`
	LastCheckedAt  time.Time   `json:"last_checked_at"`
	LastResult     string      `json:"last_result"`
	Reason         string      `json:"reason"`
	Message        string      `json:"message"`
	TargetRoutable bool        `json:"target_routable"`
}

// Evaluate returns a detached proposed state and status for each alias. It never
// changes the input snapshot, revision, or timestamps in persisted state.
func Evaluate(ctx context.Context, snap *policy.Snapshot, probe Probe) (policy.State, map[string]Status) {
	return evaluate(ctx, snap, probe, "")
}

// EvaluateAlias limits both evaluation and paid probes to one alias. An unknown
// ID returns the unchanged state and an empty status map.
func EvaluateAlias(ctx context.Context, snap *policy.Snapshot, probe Probe, aliasID string) (policy.State, map[string]Status) {
	if aliasID == "" {
		return snap.StateCopy(), map[string]Status{}
	}
	return evaluate(ctx, snap, probe, aliasID)
}

func evaluate(ctx context.Context, snap *policy.Snapshot, probe Probe, only string) (policy.State, map[string]Status) {
	state := snap.StateCopy()
	catalog := snap.CatalogCopy()
	statuses := map[string]Status{}
	now := time.Now().UTC()
	aliasNames := map[string]bool{}
	for _, alias := range state.Aliases {
		aliasNames[snap.Normalize(alias.Name)] = true
	}
	sort.Slice(catalog.Models, func(i, j int) bool { return catalog.Models[i].ID < catalog.Models[j].ID })
	for i, a := range state.Aliases {
		if only != "" && a.ID != only {
			continue
		}
		status := Status{CurrentTarget: a.Target, Candidates: []Candidate{}, LastCheckedAt: now, LastResult: "kept", TargetRoutable: snap.Routable(a.Target, a.Provider)}
		keep := func(reason, message string) {
			status.Reason = reason
			status.Message = message
			statuses[a.ID] = status
		}
		if !snap.Available() {
			keep("policy_unavailable", "Policy is unavailable.")
			continue
		}
		if !a.Enabled {
			status.LastResult = "unchanged"
			keep("disabled", "Alias is disabled.")
			continue
		}
		if a.Mode != "auto" {
			status.LastResult = "unchanged"
			keep("pinned", "Alias is pinned.")
			continue
		}
		if ctx.Err() != nil {
			keep("cancelled", "Evaluation was cancelled.")
			continue
		}
		f, ok := snap.Family(a.Auto.Family)
		if !ok {
			keep("unknown_family", "Family is unavailable.")
			continue
		}
		t, err := version.Compile(f.Template, f.AllowDated)
		if err != nil {
			keep("invalid_family", "Family template is invalid.")
			continue
		}
		current, ok := t.Parse(snap.Normalize(a.Target))
		if !ok {
			keep("current_unparseable", "Current target does not match the family.")
			continue
		}
		provider := a.Provider
		if provider == "" {
			provider = f.Provider
		}
		type entry struct {
			model policy.Model
			value version.Value
			index int
		}
		eligible := []entry{}
		seen := map[string]bool{}
		// Stable display order also makes results independent of callback map order.
		for _, m := range catalog.Models {
			n := snap.Normalize(m.ID)
			if seen[n] {
				continue
			}
			v, matches := t.Parse(n)
			if !matches {
				continue
			}
			seen[n] = true
			routable := snap.Routable(m.ID, provider)
			// Select a physical registration supporting the requested provider. A
			// normalized duplicate may otherwise sort first despite being unavailable.
			if routable {
				for _, registration := range catalog.Models {
					if snap.Normalize(registration.ID) != n || !registration.Routable {
						continue
					}
					providerMatch := provider == ""
					for _, p := range registration.Providers {
						if strings.EqualFold(p, provider) {
							providerMatch = true
						}
					}
					if providerMatch {
						m = registration
						break
					}
				}
			}
			candidate := Candidate{ID: m.ID, Version: v.Version, Date: v.Date, Prerelease: v.Prerelease, Routable: routable, Reason: "candidate"}
			switch {
			case aliasNames[n]:
				candidate.Reason = "alias_conflict"
			case v.Prerelease != "" && !a.Auto.IncludePrerelease:
				candidate.Reason = "prerelease_excluded"
			case !routable:
				candidate.Reason = "not_routable"
			default:
				eligible = append(eligible, entry{model: m, value: v, index: len(status.Candidates)})
			}
			status.Candidates = append(status.Candidates, candidate)
		}
		if !catalog.OK {
			keep("catalog_unavailable", "Catalog refresh failed; current target retained.")
			continue
		}
		if len(eligible) == 0 {
			keep("no_routable_candidates", "No eligible routable candidate exists.")
			continue
		}
		// A partial ordering cannot safely be fed to sort.Slice. Find a candidate
		// that is strictly greater than EVERY other eligible candidate instead.
		best := -1
		for j, x := range eligible {
			greater := true
			for k, y := range eligible {
				if j == k {
					continue
				}
				cmp, ordered := version.Compare(x.value, y.value)
				if !ordered || cmp <= 0 {
					greater = false
					break
				}
			}
			if greater {
				best = j
				break
			}
		}
		if best < 0 {
			keep("ambiguous", "Candidates do not have a unique strictly newest version.")
			continue
		}
		chosen := eligible[best]
		cmp, ordered := version.Compare(chosen.value, current)
		if !ordered {
			keep("ambiguous", "Candidate cannot be strictly ordered against the current target.")
			continue
		}
		if cmp <= 0 {
			status.LastResult = "unchanged"
			keep("no_newer_candidate", "Current target is already as new or newer.")
			continue
		}
		if a.Auto.HealthCheck {
			if probe == nil {
				keep("health_check_unavailable", "Health check is enabled but no probe is available.")
				continue
			}
			// Routable() may aggregate duplicate registrations. Pass only a detached
			// sanitized model to the probe, without sharing status or snapshot memory.
			probeAlias := a
			probeAlias.Provider = provider
			if err := runProbe(ctx, probe, probeAlias, chosen.model); err != nil {
				keep("health_check_failed", "Health check failed; current target retained.")
				continue
			}
		}
		if ctx.Err() != nil {
			keep("cancelled", "Evaluation was cancelled.")
			continue
		}
		status.LastResult = "updated"
		status.PreviousTarget = a.Target
		status.CurrentTarget = chosen.model.ID
		status.TargetRoutable = true
		status.Candidates[chosen.index].Chosen = true
		status.Candidates[chosen.index].Reason = "chosen"
		state.Aliases[i].Target = chosen.model.ID
		keep("newer_candidate", "A strictly newer routable candidate was selected.")
	}
	return state, statuses
}

func runProbe(ctx context.Context, probe Probe, a policy.Alias, m policy.Model) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("health probe panicked")
		}
	}()
	m.Providers = append([]string{}, m.Providers...)
	if a.RewriteResponseModel != nil {
		b := *a.RewriteResponseModel
		a.RewriteResponseModel = &b
	}
	return probe(ctx, a, m)
}
