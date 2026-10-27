package policy

import (
	"reflect"
	"strings"
	"testing"
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
