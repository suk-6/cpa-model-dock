package policy

import (
	"strings"
	"sync"
	"testing"
)

func fixture() (State, Catalog) {
	s := DefaultState()
	s.Aliases = []Alias{{ID: "al_1", Name: "gpt-sol", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol"}}
	c := Catalog{OK: true, Prefixes: []string{"team"}, Models: []Model{
		{ID: "gpt-6.1-sol", Providers: []string{"codex"}, CredentialCount: 1, Routable: true},
		{ID: "gpt-6.10-sol", Providers: []string{"codex"}, CredentialCount: 1, Routable: true},
		{ID: "gpt-6.1-sol-mini", Providers: []string{"codex"}, Routable: true},
		{ID: "gpt-7-sol-preview", Providers: []string{"codex"}, Routable: true},
		{ID: "gemini-3-pro", Providers: []string{"gemini"}, Routable: true},
	}}
	return s, c
}

func TestDefaultAndBuiltinCopies(t *testing.T) {
	s := DefaultState()
	if s.Schema != 1 || s.Revision != 0 || s.Settings.RefreshIntervalSeconds != 300 || s.Aliases == nil || s.Families == nil || s.Rules == nil {
		t.Fatalf("invalid defaults: %+v", s)
	}
	if d := Validate(s, Catalog{}); len(d) != 0 {
		t.Fatalf("invalid defaults: %+v", d)
	}
	got := BuiltinFamilies()
	if len(got) < 9 {
		t.Fatal("missing builtins")
	}
	got[0].Template = "bad"
	if BuiltinFamilies()[0].Template == "bad" {
		t.Fatal("builtins shared memory")
	}
}

func TestAllBuiltinFamilies(t *testing.T) {
	s := DefaultState()
	s.Settings.WhitelistEnabled = true
	families := BuiltinFamilies()
	for i, f := range families {
		t.Run(f.ID, func(t *testing.T) {
			s.Rules = []Rule{{ID: "r", Enabled: true, Kind: "family", Value: f.ID}}
			name := strings.ReplaceAll(f.Template, "{version}", "6.10")
			snap := New(s, Catalog{}, true)
			if !snap.Decide(name).Allowed || !snap.Decide(name+"-20261027").Allowed {
				t.Fatal("builtin family does not match", f)
			}
			for _, suffix := range []string{"-mini", "-thinking", "-preview", "-flash-lite"} {
				if snap.Decide(name + suffix).Allowed {
					t.Fatalf("family %d allowed derivative/pre %s", i, name+suffix)
				}
			}
		})
	}
}

func TestNormalizeKnownPrefixesAndNotations(t *testing.T) {
	s, c := fixture()
	snap := New(s, c, true)
	for _, tt := range []struct{ in, out string }{
		{"  GPT-6.1-SOL  ", "gpt-6.1-sol"}, {"models/GPT-6.1-SOL(high)", "gpt-6.1-sol"},
		{"TEAM/gpt-6.1-sol(8192)", "gpt-6.1-sol"}, {"team/models/gpt-6.1-sol", "gpt-6.1-sol"},
		{"models/team/gpt-6.1-sol", "gpt-6.1-sol"}, {"unknown/gpt-6.1-sol", "unknown/gpt-6.1-sol"},
		{"unknown/models/gpt-6.1-sol", "unknown/models/gpt-6.1-sol"}, {"team/team/gpt-6.1-sol", "team/gpt-6.1-sol"},
		{"models/models/gpt-6.1-sol", "models/gpt-6.1-sol"},
		{"gpt-6.1-sol(high)junk", "gpt-6.1-sol(high)junk"},
	} {
		if got := snap.Normalize(tt.in); got != tt.out {
			t.Errorf("normalize %q = %q, want %q", tt.in, got, tt.out)
		}
	}
	// No credential prefix can be guessed from a slash-bearing catalog ID.
	snap = New(s, Catalog{Models: []Model{{ID: "guessed/gpt-6.1-sol", Routable: true}}}, true)
	if got := snap.Normalize("guessed/gpt-6.1-sol"); got != "guessed/gpt-6.1-sol" {
		t.Fatal(got)
	}
}

func TestDecisionOrderAndBypassResistance(t *testing.T) {
	s, c := fixture()
	s.Settings.WhitelistEnabled = true
	s.Rules = []Rule{{ID: "r_alias", Enabled: true, Kind: "alias", Value: "GPT-SOL"}, {ID: "r_family", Enabled: true, Kind: "family", Value: "builtin:gpt-sol"}}
	snap := New(s, c, true)
	for _, name := range []string{"gpt-sol", "models/GPT-SOL(high)", "team/gpt-sol"} {
		d := snap.Decide(name)
		if !d.Allowed || d.ResolvedTarget != "gpt-6.1-sol" || d.Alias == nil {
			t.Errorf("alias %s = %+v", name, d)
		}
	}
	for _, name := range []string{"gpt-6.1-sol", "TEAM/gpt-6.1-sol", "models/gpt-6.1-sol(8192)"} {
		d := snap.Decide(name)
		if d.Allowed || d.Code != "hidden_by_alias" || d.Status != 404 {
			t.Errorf("hide bypass %s: %+v", name, d)
		}
	}
	for _, name := range []string{"gpt-6.1-sol-mini", "gpt-7-sol-preview", "unknown/gpt-6.10-sol", "team/unknown/gpt-6.10-sol", "gemini-3-pro", "team/team/gpt-6.1-sol", "models/models/gpt-6.1-sol"} {
		if d := snap.Decide(name); d.Allowed || d.Code != "not_whitelisted" {
			t.Errorf("whitelist bypass %s: %+v", name, d)
		}
	}
	if d := snap.Decide("gpt-6.10-sol"); !d.Allowed || (d.Code != "rule_allowed" || d.RuleID != "r_family") {
		t.Fatalf("new family version denied: %+v", d)
	}
	if d := snap.DecideResolved("team/gpt-6.1-sol"); d.Allowed || d.Code != "hidden_by_alias" {
		t.Fatalf("AfterAuth bypass: %+v", d)
	}
	if d := snap.DecideResolved("gpt-sol"); d.Allowed {
		t.Fatalf("resolved model reinterpreted as public alias: %+v", d)
	}
	// A family/pattern/exact rule never publishes an alias.
	for _, kind := range []string{"pattern", "exact"} {
		x := cloneState(s)
		x.Rules = []Rule{{ID: "r", Enabled: true, Kind: kind, Value: "gpt-sol"}}
		if d := New(x, c, true).Decide("gpt-sol"); d.Allowed || d.Code != "alias_not_published" {
			t.Fatalf("%s published alias: %+v", kind, d)
		}
	}
	s.Rules = append(s.Rules, Rule{ID: "r_exact", Enabled: true, Kind: "exact", Value: "gpt-6.1-sol"})
	if d := New(s, c, true).Decide("models/team/gpt-6.1-sol(high)"); !d.Allowed {
		t.Fatalf("exact allow did not override hiding: %+v", d)
	}
}

func TestWhitelistOffAndDisabledRecords(t *testing.T) {
	s, c := fixture()
	snap := New(s, c, true)
	if d := snap.Decide("other-model"); !d.Allowed || d.Code != "whitelist_disabled" {
		t.Fatal(d)
	}
	if d := snap.Decide("gpt-6.1-sol"); d.Allowed {
		t.Fatal("whitelist off bypassed original hiding")
	}
	s.Rules = []Rule{{ID: "r", Enabled: true, Kind: "exact", Value: "gpt-6.1-sol"}}
	if d := New(s, c, true).Decide("gpt-6.1-sol"); !d.Allowed {
		t.Fatal("explicit exact must override hiding even when whitelist off")
	}
	s.Aliases[0].Enabled = false
	s.Rules = nil
	if d := New(s, c, true).Decide("gpt-6.1-sol"); !d.Allowed {
		t.Fatal("disabled alias hid target")
	}
	s.Settings.WhitelistEnabled = true
	s.Rules = []Rule{{ID: "r", Enabled: false, Kind: "pattern", Value: "*"}}
	if d := New(s, c, true).Decide("gpt-6.10-sol"); d.Allowed {
		t.Fatal("disabled rule widened whitelist")
	}
	s.Rules = []Rule{}
	if d := New(s, c, true).Decide("gpt-6.10-sol"); d.Allowed {
		t.Fatal("empty allow rules must deny")
	}
}

func TestUnavailableAndProviderPins(t *testing.T) {
	s, c := fixture()
	for _, snap := range []*Snapshot{nil, {}, New(s, c, false)} {
		if d := snap.Decide("anything"); d.Allowed || d.Code != "policy_unavailable" || d.Status != 503 {
			t.Fatal(d)
		}
	}
	s.Aliases[0].Provider = "gemini"
	if d := New(s, c, true).Decide("gpt-sol"); d.Allowed || d.Code != "alias_target_unavailable" || d.Status != 503 {
		t.Fatal(d)
	}
	s.Aliases[0].Provider = "CODEX"
	if d := New(s, c, true).Decide("gpt-sol"); !d.Allowed || d.Provider != "CODEX" {
		t.Fatal(d)
	}
	// Valid persisted policy survives startup before the first catalog refresh.
	snap := New(s, Catalog{}, true)
	if !snap.Available() {
		t.Fatal("empty catalog made persisted policy unavailable")
	}
	if d := snap.Decide("gpt-sol"); d.Code != "alias_target_unavailable" {
		t.Fatal(d)
	}
	c.OK = false
	c.Models[0].Routable = false
	if d := New(s, c, true).Decide("gpt-sol"); d.Code != "alias_target_unavailable" {
		t.Fatal(d)
	}
	s.Schema = 2
	if d := New(s, c, true).Decide("anything"); d.Code != "policy_unavailable" {
		t.Fatal(d)
	}
}

func TestExactAndGlobRules(t *testing.T) {
	s, c := fixture()
	s.Aliases = nil
	s.Settings.WhitelistEnabled = true
	for _, tt := range []struct {
		kind, value, name string
		allowed           bool
	}{
		{"exact", "gpt-6.1-sol", "gpt-6.10-sol", false},
		{"exact", "models/gpt-6.1-sol(high)", "TEAM/GPT-6.1-SOL", true},
		{"pattern", "gpt-6.?-sol", "gpt-6.1-sol", true},
		{"pattern", "gpt-6.?-sol", "gpt-6.10-sol", false},
		{"pattern", "gpt-*-sol", "gpt-6.10-sol", true},
		{"pattern", "gpt-[6]-sol", "gpt-6-sol", false},
		{"pattern", "gpt-6.1-sol", "gpt-6x1-sol", false},
	} {
		s.Rules = []Rule{{ID: "r", Enabled: true, Kind: tt.kind, Value: tt.value}}
		if d := New(s, c, true).Decide(tt.name); d.Allowed != tt.allowed {
			t.Errorf("%+v: %+v", tt, d)
		}
	}
}

func TestSnapshotDeepCopy(t *testing.T) {
	s, c := fixture()
	b := true
	s.Aliases[0].RewriteResponseModel = &b
	snap := New(s, c, true)
	s.Aliases[0].Target = "wrong"
	*s.Aliases[0].RewriteResponseModel = false
	c.Models[0].Providers[0] = "wrong"
	c.Prefixes[0] = "wrong"
	snap.State.Aliases[0].Target = "wrong"
	*snap.State.Aliases[0].RewriteResponseModel = false
	snap.Catalog.Models[0].Routable = false
	snap.Catalog.Models[0].Providers[0] = "wrong"
	snap.Catalog.Prefixes[0] = "wrong"
	snap.Valid = false
	x := snap.StateCopy()
	x.Aliases[0].Target = "wrong"
	*x.Aliases[0].RewriteResponseModel = false
	y := snap.CatalogCopy()
	y.Models[0].Providers[0] = "wrong"
	y.Prefixes[0] = "wrong"
	d := snap.Decide("team/gpt-sol")
	if !d.Allowed || d.ResolvedTarget != "gpt-6.1-sol" || !*d.Alias.RewriteResponseModel {
		t.Fatal(d)
	}
	d.Alias.Target = "wrong"
	*d.Alias.RewriteResponseModel = false
	if d = snap.Decide("gpt-sol"); !d.Allowed || d.ResolvedTarget != "gpt-6.1-sol" || !*d.Alias.RewriteResponseModel {
		t.Fatal("decision alias mutated snapshot", d)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 100; j++ {
				if !snap.Decide("gpt-sol").Allowed {
					t.Error("concurrent decision denied")
				}
			}
		})
	}
	wg.Wait()
}

func TestValidationDetails(t *testing.T) {
	s, c := fixture()
	tests := []struct {
		name       string
		modify     func(*State)
		path, code string
	}{
		{"schema", func(s *State) { s.Schema = 0 }, "schema", "unsupported_schema"},
		{"revision", func(s *State) { s.Revision = -1 }, "revision", "invalid_revision"},
		{"refresh", func(s *State) { s.Settings.RefreshIntervalSeconds = 29 }, "settings.refresh_interval_seconds", "invalid_refresh_interval"},
		{"missing id", func(s *State) { s.Aliases[0].ID = "" }, "aliases[0].id", "required_id"},
		{"mode", func(s *State) { s.Aliases[0].Mode = "latest" }, "aliases[0].mode", "invalid_mode"},
		{"missing target", func(s *State) { s.Aliases[0].Target = "" }, "aliases[0].target", "required_alias_target"},
		{"unknown target", func(s *State) { s.Aliases[0].Target = "not-catalogued" }, "aliases[0].target", "unknown_target"},
		{"suffix target", func(s *State) { s.Aliases[0].Target = "gpt-6.1-sol(high)" }, "aliases[0].target", "invalid_target"},
		{"catalog collision", func(s *State) { s.Aliases[0].Name = "GPT-6.1-SOL" }, "aliases[0].name", "alias_catalog_conflict"},
		{"suffix name", func(s *State) { s.Aliases[0].Name = "gpt-sol(high)" }, "aliases[0].name", "invalid_alias_name"},
		{"prefix name", func(s *State) { s.Aliases[0].Name = "team/gpt-sol" }, "aliases[0].name", "invalid_alias_name"},
		{"duplicate alias", func(s *State) { a := s.Aliases[0]; a.ID = "al_2"; a.Name = "GPT-SOL"; s.Aliases = append(s.Aliases, a) }, "aliases[1].name", "alias_name_conflict"},
		{"duplicate ID", func(s *State) { a := s.Aliases[0]; a.Name = "other-alias"; s.Aliases = append(s.Aliases, a) }, "aliases[1].id", "duplicate_id"},
		{"chain", func(s *State) {
			a := s.Aliases[0]
			a.ID = "al_2"
			a.Name = "other-alias"
			a.Target = "gpt-sol"
			s.Aliases = append(s.Aliases, a)
		}, "aliases[1].target", "alias_target_alias"},
		{"self cycle", func(s *State) { s.Aliases[0].Target = "gpt-sol" }, "aliases[0].target", "alias_target_alias"},
		{"auto family", func(s *State) { s.Aliases[0].Mode = "auto"; s.Aliases[0].Auto.Family = "missing" }, "aliases[0].auto.family", "unknown_family"},
		{"builtin mutation", func(s *State) {
			s.Families = []Family{{ID: "builtin:gpt-sol", Name: "Other", Template: "gpt-{version}-sol"}}
		}, "families[0].id", "builtin_read_only"},
		{"template", func(s *State) { s.Families = []Family{{ID: "fam_1", Name: "Other", Template: "gpt-*"}} }, "families[0].template", "invalid_template_tokens"},
		{"rule family", func(s *State) { s.Rules = []Rule{{ID: "r", Kind: "family", Value: "missing"}} }, "rules[0].value", "unknown_family"},
		{"rule alias", func(s *State) { s.Rules = []Rule{{ID: "r", Kind: "alias", Value: "missing"}} }, "rules[0].value", "unknown_alias"},
		{"rule kind", func(s *State) { s.Rules = []Rule{{ID: "r", Kind: "regex", Value: ".*"}} }, "rules[0].kind", "invalid_rule_kind"},
		{"exact wildcard", func(s *State) { s.Rules = []Rule{{ID: "r", Kind: "exact", Value: "gpt-*"}} }, "rules[0].value", "invalid_exact"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			x := cloneState(s)
			tt.modify(&x)
			details := Validate(x, c)
			for _, d := range details {
				if d.Path == tt.path && d.Code == tt.code {
					return
				}
			}
			t.Fatalf("missing %s/%s: %+v", tt.path, tt.code, details)
		})
	}
	s.Settings.RefreshIntervalSeconds = 0
	if d := Validate(s, c); len(d) != 0 {
		t.Fatalf("zero interval inherits config: %+v", d)
	}
	s.Families = []Family{{ID: "fam_1", Name: "Custom", Template: "gpt-{version}-sol", Provider: "codex"}}
	s.Aliases[0].Mode = "auto"
	s.Aliases[0].Auto.Family = "fam_1"
	s.Aliases[0].Provider = "gemini"
	found := false
	for _, d := range Validate(s, c) {
		if d.Code == "provider_conflict" {
			found = true
		}
	}
	if !found {
		t.Fatal("provider conflict not validated")
	}
}
