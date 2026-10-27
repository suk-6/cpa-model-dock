package policy

import (
	"regexp"
	"strings"

	"github.com/cpa-model-dock/cpa-model-dock/internal/version"
)

func DefaultState() State {
	return State{Schema: 1, Settings: Settings{RefreshIntervalSeconds: 300}, Families: []Family{}, Aliases: []Alias{}, Rules: []Rule{}}
}

func BuiltinFamilies() []Family {
	return []Family{
		{ID: "builtin:gpt-sol", Name: "GPT Sol", Template: "gpt-{version}-sol", AllowDated: true},
		{ID: "builtin:gpt-astra", Name: "GPT Astra", Template: "gpt-{version}-astra", AllowDated: true},
		{ID: "builtin:gpt-luna", Name: "GPT Luna", Template: "gpt-{version}-luna", AllowDated: true},
		{ID: "builtin:claude-opus", Name: "Claude Opus", Template: "claude-opus-{version}", AllowDated: true},
		{ID: "builtin:claude-sonnet", Name: "Claude Sonnet", Template: "claude-sonnet-{version}", AllowDated: true},
		{ID: "builtin:claude-haiku", Name: "Claude Haiku", Template: "claude-haiku-{version}", AllowDated: true},
		{ID: "builtin:claude-fable", Name: "Claude Fable", Template: "claude-fable-{version}", AllowDated: true},
		{ID: "builtin:gemini-pro", Name: "Gemini Pro", Template: "gemini-{version}-pro", AllowDated: true},
		{ID: "builtin:gemini-flash", Name: "Gemini Flash", Template: "gemini-{version}-flash", AllowDated: true},
	}
}

func cloneState(s State) State {
	s.Families = append([]Family{}, s.Families...)
	s.Aliases = append([]Alias{}, s.Aliases...)
	for i := range s.Aliases {
		if p := s.Aliases[i].RewriteResponseModel; p != nil {
			b := *p
			s.Aliases[i].RewriteResponseModel = &b
		}
	}
	s.Rules = append([]Rule{}, s.Rules...)
	return s
}

func cloneCatalog(c Catalog) Catalog {
	c.Models = append([]Model{}, c.Models...)
	for i := range c.Models {
		c.Models[i].Providers = append([]string{}, c.Models[i].Providers...)
	}
	c.Prefixes = append([]string{}, c.Prefixes...)
	return c
}

// New pins independent copies of the policy and sanitized catalog. Public fields
// are compatibility views; mutating them cannot change this snapshot's decisions.
func New(s State, c Catalog, valid bool) *Snapshot {
	p := &Snapshot{state: cloneState(s), catalog: cloneCatalog(c), initialized: true}
	// Saved policy remains usable while the catalog is empty, stale, or changes.
	// Catalog-dependent save validation must not turn a missing target into a
	// global policy outage; that alias gets alias_target_unavailable instead.
	p.valid = valid && len(validate(s, Catalog{}, false)) == 0
	p.State = cloneState(p.state)
	p.Catalog = cloneCatalog(p.catalog)
	p.Valid = p.valid
	return p
}

func (s *Snapshot) StateCopy() State {
	if s == nil {
		return DefaultState()
	}
	return cloneState(s.state)
}
func (s *Snapshot) CatalogCopy() Catalog {
	if s == nil {
		return cloneCatalog(Catalog{})
	}
	return cloneCatalog(s.catalog)
}
func (s *Snapshot) Available() bool { return s != nil && s.initialized && s.valid }

// normalize strips only one known credential prefix; unknown slash spellings
// remain literal. Both prefix/models/id and models/prefix/id are supported.
func normalize(name string, prefixes []string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	if strings.HasSuffix(n, ")") {
		if i := strings.LastIndex(n, "("); i >= 0 {
			n = strings.TrimSpace(n[:i])
		}
	}
	strippedModels := strings.HasPrefix(n, "models/")
	n = strings.TrimPrefix(n, "models/")
	if i := strings.IndexByte(n, '/'); i >= 0 {
		for _, p := range prefixes {
			if p = strings.ToLower(strings.TrimSpace(p)); p != "" && n[:i] == p {
				n = n[i+1:]
				break
			}
		}
	}
	if !strippedModels {
		n = strings.TrimPrefix(n, "models/")
	}
	return n
}

func (s *Snapshot) Normalize(name string) string {
	if s == nil {
		return normalize(name, nil)
	}
	return normalize(name, s.catalog.Prefixes)
}

func (s *Snapshot) Family(id string) (Family, bool) {
	for _, f := range BuiltinFamilies() {
		if f.ID == id {
			return f, true
		}
	}
	if s != nil {
		for _, f := range s.state.Families {
			if f.ID == id {
				return f, true
			}
		}
	}
	return Family{}, false
}

// Routable respects an optional provider pin without any live host callbacks.
func (s *Snapshot) Routable(name, provider string) bool {
	if s == nil {
		return false
	}
	n := s.Normalize(name)
	for _, m := range s.catalog.Models {
		if !m.Routable || s.Normalize(m.ID) != n {
			continue
		}
		if provider == "" {
			return true
		}
		for _, p := range m.Providers {
			if strings.EqualFold(p, provider) {
				return true
			}
		}
	}
	return false
}

func decision(allowed bool, status int, code, message string, steps []string) Decision {
	return Decision{Allowed: allowed, Status: status, Code: code, Reason: message, Steps: append(steps, code)}
}

func unavailable() Decision {
	return decision(false, 503, "policy_unavailable", "Policy is unavailable.", nil)
}

func (s *Snapshot) Decide(name string) Decision {
	if !s.Available() {
		return unavailable()
	}
	n := s.Normalize(name)
	steps := []string{"normalized"}
	for _, a := range s.state.Aliases {
		if !a.Enabled || s.Normalize(a.Name) != n {
			continue
		}
		if s.state.Settings.WhitelistEnabled {
			published := false
			for _, r := range s.state.Rules {
				if r.Enabled && r.Kind == "alias" && s.Normalize(r.Value) == n {
					published = true
					break
				}
			}
			if !published {
				return decision(false, 403, "alias_not_published", "Alias is not published.", steps)
			}
		}
		if !s.Routable(a.Target, a.Provider) {
			return decision(false, 503, "alias_target_unavailable", "Alias target is unavailable.", steps)
		}
		d := decision(true, 200, "alias", "Alias target selected.", steps)
		d.ResolvedTarget = a.Target
		d.Provider = a.Provider
		// The returned alias, including its override pointer, must not alias snapshot memory.
		copied := cloneState(State{Aliases: []Alias{a}}).Aliases[0]
		d.Alias = &copied
		return d
	}
	return s.decideResolved(n, steps)
}

// DecideResolved applies target hiding and allow rules only. It deliberately does
// not reinterpret a resolved destination as another public alias (AfterAuth).
func (s *Snapshot) DecideResolved(name string) Decision {
	if !s.Available() {
		return unavailable()
	}
	return s.decideResolved(s.Normalize(name), []string{"normalized", "resolved_model"})
}

func (s *Snapshot) decideResolved(n string, steps []string) Decision {
	exact := ""
	for _, r := range s.state.Rules {
		if r.Enabled && r.Kind == "exact" && s.Normalize(r.Value) == n {
			exact = r.ID
			break
		}
	}
	for _, a := range s.state.Aliases {
		if a.Enabled && !a.ExposeOriginal && s.Normalize(a.Target) == n && exact == "" {
			return decision(false, 404, "hidden_by_alias", "Model not found.", steps)
		}
	}
	if !s.state.Settings.WhitelistEnabled {
		return decision(true, 200, "whitelist_disabled", "Whitelist is disabled.", steps)
	}
	for _, r := range s.state.Rules {
		if s.matchRuleNormalized(r, n) {
			d := decision(true, 200, "rule_allowed", "Model matches an allow rule.", steps)
			d.RuleID = r.ID
			return d
		}
	}
	return decision(false, 403, "not_whitelisted", "Model is not allowed.", steps)
}

// MatchRule supports the contract's two wildcard characters, across slash
// boundaries. All other characters are literals, never regexp syntax.
func (s *Snapshot) MatchRule(r Rule, name string) bool {
	if s == nil {
		return false
	}
	return s.matchRuleNormalized(r, s.Normalize(name))
}

func (s *Snapshot) matchRuleNormalized(r Rule, n string) bool {
	if s == nil || !r.Enabled {
		return false
	}
	switch r.Kind {
	case "exact":
		return n == s.Normalize(r.Value)
	case "pattern":
		return globMatch(s.Normalize(r.Value), n)
	case "family":
		f, ok := s.Family(r.Value)
		if !ok {
			return false
		}
		t, err := version.Compile(f.Template, f.AllowDated)
		if err != nil {
			return false
		}
		v, ok := t.Parse(n)
		if !ok || v.Prerelease != "" {
			return false
		}
		if f.Provider != "" {
			return s.Routable(n, f.Provider)
		}
		return true
	}
	return false // alias rules publish aliases; they never allow direct model IDs.
}

func globMatch(pattern, name string) bool {
	var b strings.Builder
	b.WriteString("^")
	for _, c := range pattern {
		switch c {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	return err == nil && re.MatchString(name)
}
