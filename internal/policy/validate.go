package policy

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/cpa-model-dock/cpa-model-dock/internal/version"
)

// Validate checks domain structure, name collisions, templates, and references.
// Saves must reference known catalog targets. Runtime snapshots deliberately
// skip catalog-dependent checks, so disappearance affects only that alias.
func Validate(s State, c Catalog) []Detail {
	return validate(s, c, true)
}

func validate(s State, c Catalog, checkCatalog bool) []Detail {
	out := []Detail{}
	add := func(path, code, msg string) { out = append(out, Detail{Path: path, Code: code, Message: msg}) }
	if s.Schema != 1 {
		add("schema", "unsupported_schema", "Schema must be 1.")
	}
	if s.Revision < 0 {
		add("revision", "invalid_revision", "Revision must not be negative.")
	}
	if n := s.Settings.RefreshIntervalSeconds; n != 0 && n < 30 {
		add("settings.refresh_interval_seconds", "invalid_refresh_interval", "Refresh interval must be 0 or at least 30 seconds.")
	}
	ids := map[string]bool{}
	checkID := func(id, path string) {
		if strings.TrimSpace(id) == "" {
			add(path, "required", "ID is required.")
			return
		}
		if strings.TrimSpace(id) != id || hasControl(id) {
			add(path, "invalid_id", "ID contains invalid whitespace or control characters.")
		}
		if ids[id] {
			add(path, "duplicate_id", "IDs must be unique.")
		}
		ids[id] = true
	}
	families := map[string]Family{}
	for _, f := range BuiltinFamilies() {
		families[f.ID] = f
	}
	for i, f := range s.Families {
		p := fmt.Sprintf("families[%d]", i)
		checkID(f.ID, p+".id")
		if strings.HasPrefix(strings.ToLower(f.ID), "builtin:") {
			add(p+".id", "builtin_read_only", "Built-in families are read-only.")
		}
		if _, exists := families[f.ID]; exists {
			add(p+".id", "family_conflict", "Family ID already exists.")
		}
		if strings.TrimSpace(f.Name) == "" {
			add(p+".name", "required", "Family name is required.")
		}
		if _, err := version.Compile(f.Template, f.AllowDated); err != nil {
			add(p+".template", "invalid_template", err.Error())
		}
		if strings.TrimSpace(f.Provider) != f.Provider || hasControl(f.Provider) {
			add(p+".provider", "invalid_provider", "Provider contains invalid whitespace or control characters.")
		}
		families[f.ID] = f
	}
	names := map[string]int{}
	catalogNames := map[string]bool{}
	for _, m := range c.Models {
		catalogNames[normalize(m.ID, c.Prefixes)] = true
	}
	for i, a := range s.Aliases {
		p := fmt.Sprintf("aliases[%d]", i)
		checkID(a.ID, p+".id")
		n := normalize(a.Name, c.Prefixes)
		if strings.TrimSpace(a.Name) == "" {
			add(p+".name", "required", "Alias name is required.")
		} else if n != strings.ToLower(a.Name) || strings.IndexFunc(a.Name, unicode.IsSpace) >= 0 || strings.ContainsAny(a.Name, "()*?\\") || hasControl(a.Name) {
			add(p+".name", "invalid_alias_name", "Alias name must be a canonical model name without whitespace, wildcards, or thinking suffixes.")
		}
		if _, exists := names[n]; exists {
			add(p+".name", "alias_conflict", "Alias names must be unique ignoring case.")
		}
		names[n] = i
		if checkCatalog && catalogNames[n] {
			add(p+".name", "alias_conflict", "Alias name conflicts with a catalog model.")
		}
		if a.Mode != "pinned" && a.Mode != "auto" {
			add(p+".mode", "invalid_mode", "Alias mode must be pinned or auto.")
		}
		if strings.TrimSpace(a.Target) == "" {
			add(p+".target", "required", "Alias target is required.")
		} else if strings.TrimSpace(a.Target) != a.Target || strings.IndexFunc(a.Target, unicode.IsSpace) >= 0 || strings.ContainsAny(a.Target, "()*?\\") || hasControl(a.Target) {
			add(p+".target", "invalid_target", "Target must be a model ID without whitespace, wildcards, or thinking suffixes.")
		}
		if strings.TrimSpace(a.Provider) != a.Provider || hasControl(a.Provider) {
			add(p+".provider", "invalid_provider", "Provider contains invalid whitespace or control characters.")
		}
		if checkCatalog && a.Target != "" && !catalogNames[normalize(a.Target, c.Prefixes)] {
			add(p+".target", "unknown_target", "Alias target must reference a known catalog model.")
		}
		if a.Mode == "auto" {
			f, ok := families[a.Auto.Family]
			if !ok {
				add(p+".auto.family", "unknown_family", "Auto alias must reference an existing family.")
			} else if f.Provider != "" && a.Provider != "" && !strings.EqualFold(f.Provider, a.Provider) {
				add(p+".provider", "provider_conflict", "Alias and family provider restrictions conflict.")
			}
		}
	}
	for i, a := range s.Aliases {
		if _, exists := names[normalize(a.Target, c.Prefixes)]; exists {
			add(fmt.Sprintf("aliases[%d].target", i), "alias_target_alias", "Alias targets must be real model IDs, not other aliases.")
		}
	}
	for i, r := range s.Rules {
		p := fmt.Sprintf("rules[%d]", i)
		checkID(r.ID, p+".id")
		if strings.TrimSpace(r.Value) == "" {
			add(p+".value", "required", "Rule value is required.")
		}
		if hasControl(r.Value) {
			add(p+".value", "invalid_value", "Rule value contains control characters.")
		}
		switch r.Kind {
		case "exact":
			if strings.ContainsAny(r.Value, "*?") {
				add(p+".value", "invalid_exact", "Exact rules cannot contain wildcard characters.")
			}
		case "pattern":
			if strings.ContainsAny(r.Value, "\\()") {
				add(p+".value", "invalid_pattern", "Pattern supports only literal model characters and * or ? wildcards.")
			}
		case "family":
			if _, ok := families[r.Value]; !ok {
				add(p+".value", "unknown_family", "Rule must reference an existing family.")
			}
		case "alias":
			if _, ok := names[normalize(r.Value, c.Prefixes)]; !ok {
				add(p+".value", "unknown_alias", "Rule must reference an existing alias.")
			}
		default:
			add(p+".kind", "invalid_rule_kind", "Rule kind must be exact, family, pattern, or alias.")
		}
	}
	return out
}

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }
