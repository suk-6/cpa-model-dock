package policy

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSemanticHistorySummary(t *testing.T) {
	before := DefaultState()
	before.Aliases = []Alias{{ID: "b", Name: "removed", Target: "old"}, {ID: "a", Name: "old-name", Enabled: true, Mode: "pinned", Target: "v1"}}
	before.Rules = []Rule{{ID: "r", Enabled: true, Kind: "exact", Value: "v1"}}
	after := cloneState(before)
	after.Aliases = []Alias{{ID: "a", Name: "new-name", Enabled: false, Mode: "auto", Target: "v2"}, {ID: "c", Name: "created", Target: "v3"}}
	after.Rules = []Rule{{ID: "r", Enabled: false, Kind: "pattern", Value: "v*"}}
	after.Settings.WhitelistEnabled = true
	after.Settings.RewriteResponseModel = true
	after.Families = []Family{{ID: "f", Name: "family", Template: "v{version}"}}
	detail := HistorySummary(before, after, "user", "Updated model policy")
	if detail.Code != "changes" {
		t.Fatal(detail)
	}
	changes := detail.Params["changes"].([]Text)
	codes := []string{}
	for _, c := range changes {
		codes = append(codes, c.Code)
		if c.Params == nil || c.Message == "" {
			t.Fatal(c)
		}
	}
	want := []string{"alias_renamed", "alias_disabled", "alias_target_changed", "alias_changed", "alias_deleted", "alias_created", "rule_disabled", "rule_changed", "whitelist_enabled", "settings_changed", "families_changed"}
	if !reflect.DeepEqual(codes, want) || detail.Params["count"] != len(want) || !strings.HasPrefix(detail.Message, "11 changes: ") {
		t.Fatal(detail, codes)
	}
	if !reflect.DeepEqual(changes[0].Params, map[string]any{"alias_id": "a", "from": "old-name", "to": "new-name"}) {
		t.Fatal(changes[0])
	}
	if changes[2].Params["from"] != "v1" || changes[2].Params["to"] != "v2" {
		t.Fatal(changes[2])
	}
	// Array order cannot change the semantic summary.
	after.Aliases[0], after.Aliases[1] = after.Aliases[1], after.Aliases[0]
	before.Aliases[0], before.Aliases[1] = before.Aliases[1], before.Aliases[0]
	if !reflect.DeepEqual(detail, HistorySummary(before, after, "user", "different old prose")) {
		t.Fatal("summary depends on input array order or old prose")
	}
}

func TestAliasSettingHistoryIncludesTypedFieldDiffs(t *testing.T) {
	before := DefaultState()
	before.Aliases = []Alias{{ID: "a", Name: "sol", Mode: "pinned", ExposeOriginal: true}}
	after := cloneState(before)
	on := true
	after.Aliases[0].Mode = "auto"
	after.Aliases[0].ExposeOriginal = false
	after.Aliases[0].RewriteResponseModel = &on
	detail := HistorySummary(before, after, "user", "")
	want := []map[string]any{{"field": "mode", "from": "pinned", "to": "auto"}, {"field": "expose_original", "from": true, "to": false}, {"field": "rewrite_response_model", "from": nil, "to": true}}
	if detail.Code != "alias_changed" || !reflect.DeepEqual(detail.Params["fields"], want) || detail.Params["count"] != 3 {
		t.Fatal(detail)
	}
	for _, text := range []string{"mode: pinned → automatic", "original name: true → false", "response name: default → on"} {
		if !strings.Contains(detail.Message, text) {
			t.Fatal(detail.Message)
		}
	}
	after.Aliases[0].Provider = strings.Repeat("한", 100)
	after.Aliases[0].Auto.Family = "family"
	detail = HistorySummary(before, after, "user", "")
	if !strings.Contains(detail.Message, "+2 more") || !strings.Contains(detail.Message, "…") || strings.Contains(detail.Message, strings.Repeat("한", 61)) {
		t.Fatal(detail)
	}
	// Equivalent override pointers have the same semantic value.
	before = cloneState(after)
	after.Aliases[0].RewriteResponseModel = new(bool)
	*after.Aliases[0].RewriteResponseModel = true
	if d := HistorySummary(before, after, "user", ""); d.Code != "policy_unchanged" {
		t.Fatal(d)
	}
}

func TestRestoreHistoryHumanSummariesAndProjection(t *testing.T) {
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	detail := RestoreHistorySummary("state", at, nil)
	if detail.Code != "policy_restored" || detail.Params["at"] != "2026-10-27T12:00:00Z" {
		t.Fatal(detail)
	}
	aliases := []Alias{{ID: "a", Name: "sol", Target: "v1", Mode: "pinned"}, {ID: "b", Name: "other", Target: "v2", Mode: "auto"}}
	detail = RestoreHistorySummary("alias_target", at, aliases)
	projected := HistoryEntrySummary(State{}, State{Aliases: aliases[1:]}, "user", detail.Message, &detail)
	if projected.Code != "alias_target_restored" || projected.Params["alias"] != "other" || projected.Params["target"] != "v2" || projected.Params["pinned"] != false {
		t.Fatal(projected)
	}
	if !strings.Contains(projected.Message, "Restored alias other target to v2") {
		t.Fatal(projected)
	}
}

func TestHistorySummarySpecificEventsAndRestore(t *testing.T) {
	before := DefaultState()
	before.Aliases = []Alias{{ID: "a", Name: "stable", Enabled: false, Mode: "pinned", Target: "v1"}}
	before.Rules = []Rule{{ID: "r", Enabled: false, Kind: "exact", Value: "v1"}}
	tests := []struct {
		code           string
		mutate         func(*State)
		actor, summary string
	}{
		{"alias_enabled", func(s *State) { s.Aliases[0].Enabled = true }, "user", ""},
		{"alias_target_changed", func(s *State) { s.Aliases[0].Target = "v2" }, "user", ""},
		{"alias_target_auto_updated", func(s *State) { s.Aliases[0].Target = "v2" }, "auto", ""},
		{"rule_added", func(s *State) { s.Rules = append(s.Rules, Rule{ID: "new", Kind: "alias", Value: "stable"}) }, "user", ""},
		{"rule_removed", func(s *State) { s.Rules = nil }, "user", ""},
		{"rule_enabled", func(s *State) { s.Rules[0].Enabled = true }, "user", ""},
		{"restored", func(s *State) { s.Aliases[0].Target = "v2" }, "user", "Restored before history entry 12:a"},
		{"policy_unchanged", func(s *State) { s.Revision = 42 }, "user", "Updated model policy"},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			after := cloneState(before)
			tt.mutate(&after)
			d := HistorySummary(before, after, tt.actor, tt.summary)
			if d.Code != tt.code {
				t.Fatal(d)
			}
			if tt.code == "restored" && d.Params["entry_id"] != "12:a" {
				t.Fatal(d)
			}
		})
	}
}
