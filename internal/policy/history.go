package policy

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// HistorySummary derives semantic changes in stable ID order. The input summary
// is only used for legacy restore metadata; English prose is never a diff key.
func HistorySummary(before, after State, actor, summary string) Text {
	for _, prefix := range []string{"Restored before history entry ", "Restored history entry "} {
		if id, ok := strings.CutPrefix(summary, prefix); ok {
			return NewText("restored", "Restored history entry "+id, map[string]any{"entry_id": id})
		}
	}
	changes := []Text{}
	add := func(code, message string, p map[string]any) { changes = append(changes, NewText(code, message, p)) }
	oldAliases, newAliases := map[string]Alias{}, map[string]Alias{}
	ids := map[string]bool{}
	for _, a := range before.Aliases {
		oldAliases[a.ID] = a
		ids[a.ID] = true
	}
	for _, a := range after.Aliases {
		newAliases[a.ID] = a
		ids[a.ID] = true
	}
	ordered := func(ids map[string]bool) []string {
		keys := []string{}
		for id := range ids {
			keys = append(keys, id)
		}
		sort.Strings(keys)
		return keys
	}
	for _, id := range ordered(ids) {
		old, had := oldAliases[id]
		next, has := newAliases[id]
		p := map[string]any{"alias_id": id, "alias": next.Name}
		switch {
		case !had:
			p["target"] = next.Target
			add("alias_created", fmt.Sprintf("Created alias %s → %s", next.Name, next.Target), p)
		case !has:
			add("alias_deleted", "Deleted alias "+old.Name, map[string]any{"alias_id": id, "alias": old.Name})
		default:
			if old.Name != next.Name {
				add("alias_renamed", fmt.Sprintf("Renamed alias %s to %s", old.Name, next.Name), map[string]any{"alias_id": id, "from": old.Name, "to": next.Name})
			}
			if old.Enabled != next.Enabled {
				code, verb := "alias_disabled", "Disabled"
				if next.Enabled {
					code, verb = "alias_enabled", "Enabled"
				}
				add(code, verb+" alias "+next.Name, p)
			}
			if old.Target != next.Target {
				code, verb := "alias_target_changed", "Changed"
				if actor == "auto" {
					code, verb = "alias_target_auto_updated", "Automatically updated"
				}
				add(code, fmt.Sprintf("%s alias %s target from %s to %s", verb, next.Name, old.Target, next.Target), map[string]any{"alias_id": id, "alias": next.Name, "from": old.Target, "to": next.Target})
			}
			old.Name, old.Enabled, old.Target = next.Name, next.Enabled, next.Target
			if !reflect.DeepEqual(old, next) {
				add("alias_changed", "Changed alias "+next.Name, p)
			}
		}
	}
	oldRules, newRules := map[string]Rule{}, map[string]Rule{}
	ids = map[string]bool{}
	for _, r := range before.Rules {
		oldRules[r.ID] = r
		ids[r.ID] = true
	}
	for _, r := range after.Rules {
		newRules[r.ID] = r
		ids[r.ID] = true
	}
	for _, id := range ordered(ids) {
		old, had := oldRules[id]
		next, has := newRules[id]
		r := next
		if !has {
			r = old
		}
		p := map[string]any{"rule_id": id, "kind": r.Kind, "value": r.Value}
		label := fmt.Sprintf("%s rule %s (%s)", r.Kind, r.Value, id)
		switch {
		case !had:
			add("rule_added", "Added "+label, p)
		case !has:
			add("rule_removed", "Removed "+label, p)
		default:
			if old.Enabled != next.Enabled {
				code, verb := "rule_disabled", "Disabled"
				if next.Enabled {
					code, verb = "rule_enabled", "Enabled"
				}
				add(code, verb+" "+label, p)
			}
			old.Enabled = next.Enabled
			if old != next {
				add("rule_changed", "Changed "+label, p)
			}
		}
	}
	if before.Settings.WhitelistEnabled != after.Settings.WhitelistEnabled {
		code, msg := "whitelist_disabled", "Disabled whitelist"
		if after.Settings.WhitelistEnabled {
			code, msg = "whitelist_enabled", "Enabled whitelist"
		}
		add(code, msg, nil)
	}
	oldSettings := before.Settings
	oldSettings.WhitelistEnabled = after.Settings.WhitelistEnabled
	if oldSettings != after.Settings {
		add("settings_changed", "Changed settings", nil)
	}
	// Array order is not a semantic family change.
	oldFamilies := append([]Family{}, before.Families...)
	newFamilies := append([]Family{}, after.Families...)
	sort.Slice(oldFamilies, func(i, j int) bool { return oldFamilies[i].ID < oldFamilies[j].ID })
	sort.Slice(newFamilies, func(i, j int) bool { return newFamilies[i].ID < newFamilies[j].ID })
	if !reflect.DeepEqual(oldFamilies, newFamilies) {
		add("families_changed", "Changed model families", nil)
	}
	switch len(changes) {
	case 0:
		return NewText("policy_unchanged", "Saved policy without changes")
	case 1:
		return changes[0]
	default:
		messages := make([]string, len(changes))
		for i, c := range changes {
			messages[i] = c.Message
		}
		return NewText("changes", fmt.Sprintf("%d changes: %s", len(changes), strings.Join(messages, "; ")), map[string]any{"count": len(changes), "changes": changes})
	}
}
