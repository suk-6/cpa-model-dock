package autoupdate

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
)

func fixture(current string, names ...string) (*policy.Snapshot, policy.State, policy.Catalog) {
	s := policy.DefaultState()
	s.Aliases = []policy.Alias{{ID: "al_1", Name: "gpt-sol", Enabled: true, Mode: "auto", Target: current, Auto: policy.Auto{Family: "builtin:gpt-sol"}}}
	c := policy.Catalog{OK: true, Models: []policy.Model{}}
	for _, name := range names {
		c.Models = append(c.Models, policy.Model{ID: name, Providers: []string{"codex"}, CredentialCount: 1, Routable: true})
	}
	return policy.New(s, c, true), s, c
}

func TestNumericUpgradeAndNoDerivatives(t *testing.T) {
	snap, _, _ := fixture("gpt-6.9-sol", "gpt-6.9-sol", "gpt-6.10-sol", "gpt-99-sol-mini", "gpt-99-sol-codex", "gpt-99-sol-vision")
	next, statuses := Evaluate(context.Background(), snap, nil)
	status := statuses["al_1"]
	if next.Aliases[0].Target != "gpt-6.10-sol" || status.LastResult != "updated" || status.PreviousTarget != "gpt-6.9-sol" {
		t.Fatalf("%+v %+v", next, status)
	}
	if len(status.Candidates) != 2 || !status.TargetRoutable || status.LastCheckedAt.IsZero() {
		t.Fatal(status)
	}
	chosen := 0
	for _, c := range status.Candidates {
		if c.Chosen {
			chosen++
			if c.ID != "gpt-6.10-sol" {
				t.Fatal(c)
			}
		}
	}
	if chosen != 1 {
		t.Fatal(status)
	}
	if snap.StateCopy().Aliases[0].Target != "gpt-6.9-sol" {
		t.Fatal("input mutated")
	}
	if next.Revision != snap.StateCopy().Revision || !next.UpdatedAt.Equal(snap.StateCopy().UpdatedAt) {
		t.Fatal("evaluator must not persist or bump revision")
	}
}

func TestPrereleaseExcludedUnlessEnabled(t *testing.T) {
	_, s, c := fixture("gpt-6.9-sol", "gpt-6.10-sol", "gpt-7-sol-preview", "gpt-8-sol-beta", "gpt-9-sol-experimental", "gpt-10-sol-nightly")
	next, statuses := Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "gpt-6.10-sol" {
		t.Fatal(next)
	}
	excluded := 0
	for _, candidate := range statuses["al_1"].Candidates {
		if candidate.Reason == "prerelease_excluded" {
			excluded++
		}
	}
	if excluded != 4 {
		t.Fatal(statuses)
	}
	s.Aliases[0].Auto.IncludePrerelease = true
	next, _ = Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "gpt-10-sol-nightly" {
		t.Fatal(next)
	}
	// A stable release can replace its prerelease at the same version.
	snap, _, _ := fixture("gpt-6.10-sol-preview", "gpt-6.10-sol")
	next, statuses = Evaluate(context.Background(), snap, nil)
	if next.Aliases[0].Target != "gpt-6.10-sol" || statuses["al_1"].LastResult != "updated" {
		t.Fatal(next, statuses)
	}
}

func TestKeepCasesAndNoDowngrade(t *testing.T) {
	for _, tt := range []struct {
		name, current  string
		models         []string
		reason, result string
	}{
		{"downgrade", "gpt-7-sol", []string{"gpt-6.10-sol"}, "no_newer_candidate", "unchanged"},
		{"same version", "gpt-6.10-sol", []string{"gpt-6.10-sol"}, "no_newer_candidate", "unchanged"},
		{"same numeric version", "gpt-6-10-sol", []string{"gpt-6.10-sol"}, "no_newer_candidate", "unchanged"},
		{"current unparseable", "custom-model", []string{"gpt-7-sol"}, "current_unparseable", "kept"},
		{"empty candidates", "gpt-6.1-sol", nil, "no_routable_candidates", "kept"},
		{"dated undated", "gpt-6.1-sol", []string{"gpt-7-sol", "gpt-7-sol-20261101"}, "ambiguous", "kept"},
		{"equal different spellings", "gpt-6.1-sol", []string{"gpt-7.1-sol", "gpt-7-1-sol"}, "ambiguous", "kept"},
		{"candidate current dates ambiguous", "gpt-6.1-sol", []string{"gpt-6.1-sol-20261101"}, "ambiguous", "kept"},
		{"equal prerelease labels ambiguous", "gpt-6-sol", []string{"gpt-7-sol-alpha", "gpt-7-sol-beta"}, "no_routable_candidates", "kept"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			snap, _, _ := fixture(tt.current, tt.models...)
			next, results := Evaluate(context.Background(), snap, nil)
			status := results["al_1"]
			if next.Aliases[0].Target != tt.current || status.Reason != tt.reason || status.LastResult != tt.result {
				t.Fatalf("target=%s status=%+v", next.Aliases[0].Target, status)
			}
		})
	}
	// Lower ambiguous pairs do not prevent a uniquely newer version winning.
	snap, _, _ := fixture("gpt-5-sol", "gpt-6-sol", "gpt-6-sol-20261101", "gpt-7-sol")
	next, status := Evaluate(context.Background(), snap, nil)
	if next.Aliases[0].Target != "gpt-7-sol" || status["al_1"].LastResult != "updated" {
		t.Fatal(next, status)
	}
}

func TestRoutabilityAndProviderRestrictions(t *testing.T) {
	_, s, c := fixture("gpt-6-sol", "gpt-7-sol", "gpt-8-sol", "gpt-9-sol")
	c.Models[2].Routable = false
	c.Models[1].Providers = []string{"gemini"}
	s.Aliases[0].Provider = "codex"
	next, statuses := Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "gpt-7-sol" {
		t.Fatal(next)
	}
	for _, candidate := range statuses["al_1"].Candidates {
		if candidate.ID != "gpt-7-sol" && candidate.Routable {
			t.Fatal(candidate)
		}
	}
	s.Aliases[0].Provider = ""
	s.Families = []policy.Family{{ID: "fam_custom", Name: "Custom", Template: "gpt-{version}-sol", Provider: "codex"}}
	s.Aliases[0].Auto.Family = "fam_custom"
	next, _ = Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "gpt-7-sol" {
		t.Fatal("family provider restriction ignored", next)
	}
	// Unroutable current target never permits a downgrade.
	snap, _, _ := fixture("gpt-10-sol", "gpt-7-sol")
	next, statuses = Evaluate(context.Background(), snap, nil)
	if next.Aliases[0].Target != "gpt-10-sol" || statuses["al_1"].TargetRoutable {
		t.Fatal(next, statuses)
	}
}

func TestFailureKeepsAndHealthProbes(t *testing.T) {
	_, s, c := fixture("gpt-6-sol", "gpt-7-sol", "gpt-8-sol")
	s.Aliases[0].Auto.HealthCheck = true
	for _, tt := range []struct {
		name   string
		probe  Probe
		reason string
	}{
		{"missing probe", nil, "health_check_unavailable"},
		{"failed probe", func(context.Context, policy.Alias, policy.Model) error {
			return errors.New("secret token must not leak")
		}, "health_check_failed"},
		{"panic probe", func(context.Context, policy.Alias, policy.Model) error { panic("secret") }, "health_check_failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			next, status := Evaluate(context.Background(), policy.New(s, c, true), tt.probe)
			if next.Aliases[0].Target != "gpt-6-sol" || status["al_1"].Reason != tt.reason {
				t.Fatal(next, status)
			}
			if strings.Contains(status["al_1"].Message, "secret") {
				t.Fatal("probe error leaked into status")
			}
		})
	}
	calls := 0
	success := func(_ context.Context, a policy.Alias, m policy.Model) error {
		calls++
		if a.ID != "al_1" || m.ID != "gpt-8-sol" {
			t.Fatalf("wrong probe: %+v %+v", a, m)
		}
		return nil
	}
	next, status := Evaluate(context.Background(), policy.New(s, c, true), success)
	if calls != 1 || next.Aliases[0].Target != "gpt-8-sol" || status["al_1"].LastResult != "updated" {
		t.Fatal(calls, next, status)
	}
	s.Aliases[0].Auto.HealthCheck = false
	calls = 0
	Evaluate(context.Background(), policy.New(s, c, true), success)
	if calls != 0 {
		t.Fatal("opt-out probe called")
	}
	c.OK = false
	next, status = Evaluate(context.Background(), policy.New(s, c, true), success)
	if next.Aliases[0].Target != "gpt-6-sol" || status["al_1"].Reason != "catalog_unavailable" || calls != 0 {
		t.Fatal(next, status, calls)
	}
	c.OK = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	next, status = Evaluate(ctx, policy.New(s, c, true), success)
	if next.Aliases[0].Target != "gpt-6-sol" || status["al_1"].Reason != "cancelled" {
		t.Fatal(next, status)
	}
	s.Aliases[0].Auto.HealthCheck = true
	ctx, cancel = context.WithCancel(context.Background())
	next, status = Evaluate(ctx, policy.New(s, c, true), func(context.Context, policy.Alias, policy.Model) error { cancel(); return nil })
	if next.Aliases[0].Target != "gpt-6-sol" || status["al_1"].Reason != "cancelled" {
		t.Fatal(next, status)
	}
}

func TestCatalogAliasConflictAndDuplicateRegistrations(t *testing.T) {
	_, s, c := fixture("gpt-6-sol", "gpt-7-sol", "gpt-8-sol")
	s.Aliases = append(s.Aliases, policy.Alias{ID: "al_2", Name: "gpt-8-sol", Mode: "pinned", Target: "gpt-6-sol"})
	next, status := Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "gpt-7-sol" {
		t.Fatal("automatic target became another alias", next, status)
	}
	found := false
	for _, candidate := range status["al_1"].Candidates {
		if candidate.ID == "gpt-8-sol" && candidate.Reason == "alias_conflict" {
			found = true
		}
	}
	if !found {
		t.Fatal("catalog alias collision missing from candidates", status)
	}
	// Differently prefixed registrations of one model do not create ambiguous
	// versions, and the probe receives a physically routable registration.
	_, s, c = fixture("gpt-6-sol", "gpt-7-sol", "team/gpt-7-sol")
	c.Prefixes = []string{"team"}
	c.Models[0].Routable = false
	c.Models[1].Providers = []string{"codex"}
	s.Aliases[0].Provider = "codex"
	s.Aliases[0].Auto.HealthCheck = true
	calls := 0
	next, status = Evaluate(context.Background(), policy.New(s, c, true), func(_ context.Context, a policy.Alias, m policy.Model) error {
		calls++
		if !m.Routable || m.ID != "team/gpt-7-sol" || a.Provider != "codex" {
			t.Fatalf("invalid physical probe candidate: %+v %+v", a, m)
		}
		return nil
	})
	if calls != 1 || next.Aliases[0].Target != "team/gpt-7-sol" || len(status["al_1"].Candidates) != 1 {
		t.Fatal(calls, next, status)
	}
}

func TestSingleAliasHealthProbeUsesEffectiveFamilyProvider(t *testing.T) {
	_, s, c := fixture("gpt-6-sol", "gpt-7-sol")
	s.Families = []policy.Family{{ID: "fam_1", Name: "Pinned provider", Template: "gpt-{version}-sol", Provider: "codex"}}
	s.Aliases[0].Auto = policy.Auto{Family: "fam_1", HealthCheck: true}
	other := s.Aliases[0]
	other.ID = "al_2"
	other.Name = "another-sol"
	s.Aliases = append(s.Aliases, other)
	calls := 0
	next, status := EvaluateAlias(context.Background(), policy.New(s, c, true), func(_ context.Context, a policy.Alias, m policy.Model) error {
		calls++
		if a.ID != "al_2" || a.Provider != "codex" || m.ID != "gpt-7-sol" {
			t.Fatalf("wrong scoped probe: %+v %+v", a, m)
		}
		return nil
	}, "al_2")
	if calls != 1 || len(status) != 1 || next.Aliases[0].Target != "gpt-6-sol" || next.Aliases[1].Target != "gpt-7-sol" || next.Aliases[1].Provider != "" {
		t.Fatal(calls, next, status)
	}
}

func TestDisabledPinnedInvalidAndAliasSelection(t *testing.T) {
	_, s, c := fixture("gpt-6-sol", "gpt-7-sol")
	s.Aliases[0].Enabled = false
	next, status := Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "gpt-6-sol" || status["al_1"].Reason != "disabled" {
		t.Fatal(next, status)
	}
	s.Aliases[0].Enabled = true
	s.Aliases[0].Mode = "pinned"
	_, status = Evaluate(context.Background(), policy.New(s, c, true), nil)
	if status["al_1"].Reason != "pinned" {
		t.Fatal(status)
	}
	_, status = Evaluate(context.Background(), policy.New(s, c, false), nil)
	if status["al_1"].Reason != "policy_unavailable" {
		t.Fatal(status)
	}
	s.Aliases[0].Mode = "auto"
	other := s.Aliases[0]
	other.ID = "al_2"
	other.Name = "other-sol"
	s.Aliases = append(s.Aliases, other)
	snap := policy.New(s, c, true)
	next, status = EvaluateAlias(context.Background(), snap, nil, "al_2")
	if len(status) != 1 || next.Aliases[0].Target != "gpt-6-sol" || next.Aliases[1].Target != "gpt-7-sol" {
		t.Fatal(next, status)
	}
	for _, id := range []string{"", "nonexistent"} {
		next, status = EvaluateAlias(context.Background(), snap, nil, id)
		if len(status) != 0 || next.Aliases[1].Target != "gpt-6-sol" {
			t.Fatal(next, status)
		}
	}
}

func TestDatedBuiltinsAndExplicitCustomFamily(t *testing.T) {
	s := policy.DefaultState()
	s.Aliases = []policy.Alias{{ID: "al_1", Name: "opus", Enabled: true, Mode: "auto", Target: "claude-opus-4-5-20251101", Auto: policy.Auto{Family: "builtin:claude-opus"}}}
	c := policy.Catalog{OK: true, Models: []policy.Model{{ID: "claude-opus-4.5-20251201", Routable: true}, {ID: "claude-opus-4-5-20251101", Routable: true}}}
	next, status := Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "claude-opus-4.5-20251201" || status["al_1"].LastResult != "updated" {
		t.Fatal(next, status)
	}
	s.Families = []policy.Family{{ID: "fam_1", Name: "Custom", Template: "custom-{version}-x-{date}"}}
	s.Aliases[0].Auto.Family = "fam_1"
	s.Aliases[0].Target = "custom-2.9-x-20251101"
	c.Models = []policy.Model{{ID: "custom-2.10-x-20251001", Routable: true}, {ID: "custom-2.9-x-20251101", Routable: true}, {ID: "custom-99-x-20251101-mini", Routable: true}}
	next, _ = Evaluate(context.Background(), policy.New(s, c, true), nil)
	if next.Aliases[0].Target != "custom-2.10-x-20251001" {
		t.Fatal(next)
	}
}
