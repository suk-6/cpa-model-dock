package policy

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
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
			if fields := aliasFieldChanges(old, next); len(fields) > 0 {
				p := map[string]any{"alias_id": id, "alias": next.Name, "fields": fields, "count": len(fields)}
				messages := make([]string, 0, len(fields))
				for _, field := range fields[:min(3, len(fields))] {
					name := field["field"].(string)
					messages = append(messages, fmt.Sprintf("%s: %s → %s", aliasFieldLabel(name), aliasFieldValue(name, field["from"]), aliasFieldValue(name, field["to"])))
				}
				if len(fields) > 3 {
					messages = append(messages, fmt.Sprintf("+%d more", len(fields)-3))
				}
				add("alias_changed", "Changed alias "+next.Name+": "+strings.Join(messages, "; "), p)
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

func aliasFieldLabel(field string) string {
	return map[string]string{"mode": "mode", "provider": "provider", "expose_original": "original name", "rewrite_response_model": "response name", "auto.family": "family", "auto.include_prerelease": "prereleases", "auto.health_check": "model test"}[field]
}

func aliasFieldValue(field string, value any) string {
	if value == nil {
		return "default"
	}
	if field == "mode" && value == "auto" {
		return "automatic"
	}
	if field == "rewrite_response_model" {
		if value == true {
			return "on"
		}
		return "off"
	}
	text := []rune(fmt.Sprint(value))
	if len(text) > 60 {
		return string(text[:59]) + "…"
	}
	return string(text)
}

// Field values remain typed in params so clients can localize names and values.
func aliasFieldChanges(old, next Alias) []map[string]any {
	fields := []map[string]any{}
	add := func(field string, from, to any) {
		if !reflect.DeepEqual(from, to) {
			fields = append(fields, map[string]any{"field": field, "from": from, "to": to})
		}
	}
	response := func(value *bool) any {
		if value == nil {
			return nil
		}
		return *value
	}
	add("mode", old.Mode, next.Mode)
	add("provider", old.Provider, next.Provider)
	add("expose_original", old.ExposeOriginal, next.ExposeOriginal)
	add("rewrite_response_model", response(old.RewriteResponseModel), response(next.RewriteResponseModel))
	add("auto.family", old.Auto.Family, next.Auto.Family)
	add("auto.include_prerelease", old.Auto.IncludePrerelease, next.Auto.IncludePrerelease)
	add("auto.health_check", old.Auto.HealthCheck, next.Auto.HealthCheck)
	return fields
}

// RestoreHistorySummary describes the selected historical version without exposing IDs.
// aliases contains only the aliases restored by an alias_target operation.
func RestoreHistorySummary(scope string, at time.Time, aliases []Alias) Text {
	if scope == "state" {
		stamp := at.UTC().Format(time.RFC3339)
		return NewText("policy_restored", "Restored policy from "+stamp, map[string]any{"at": stamp})
	}
	changes := []Text{}
	aliases = append([]Alias{}, aliases...)
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].ID < aliases[j].ID })
	for _, alias := range aliases {
		changes = append(changes, NewText("alias_target_restored", fmt.Sprintf("Restored alias %s target to %s", alias.Name, alias.Target), map[string]any{"alias_id": alias.ID, "alias": alias.Name, "target": alias.Target, "pinned": alias.Mode == "pinned"}))
	}
	if len(changes) == 1 {
		return changes[0]
	}
	messages := []string{}
	for _, change := range changes {
		messages = append(messages, change.Message)
	}
	return NewText("changes", fmt.Sprintf("%d changes: %s", len(changes), strings.Join(messages, "; ")), map[string]any{"count": len(changes), "changes": changes})
}

// HistoryEntrySummary refreshes ordinary legacy diffs while retaining restore intent.
// For management's per-alias rows, select only that alias's persisted restore detail.
func HistoryEntrySummary(before, after State, actor, summary string, persisted *Text) Text {
	if persisted != nil {
		if persisted.Code == "policy_restored" || persisted.Code == "alias_target_restored" {
			return *persisted
		}
		if persisted.Code == "changes" && len(after.Aliases) == 1 {
			// Persisted nested params may have passed through encoding/json.
			data, _ := json.Marshal(persisted.Params["changes"])
			var changes []Text
			_ = json.Unmarshal(data, &changes)
			for _, change := range changes {
				if change.Code == "alias_target_restored" && change.Params["alias_id"] == after.Aliases[0].ID {
					return change
				}
			}
		}
	}
	return HistorySummary(before, after, actor, summary)
}
