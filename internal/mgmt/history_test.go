package mgmt

import (
	"reflect"
	"testing"

	"github.com/suk-6/cpa-model-dock/internal/policy"
)

func TestHistoryRestorePinCallbackAndCompatibility(t *testing.T) {
	for _, pin := range []bool{false, true} {
		f := setup(t)
		called := false
		f.h.config.RestorePinned = func(expected int64, id, scope string, gotPin bool) (policy.State, error) {
			called = true
			if expected != f.current.Load().StateCopy().Revision || id != "42:alias" || scope != "alias_target" || gotPin != pin {
				t.Fatalf("callback: %d %s %s %v", expected, id, scope, gotPin)
			}
			return f.current.Load().StateCopy(), nil
		}
		decode(t, call(t, f.h, "POST", "/history/restore", map[string]any{"revision": f.current.Load().StateCopy().Revision, "entry_id": "42:alias", "scope": "alias_target", "pin": pin}), 200)
		if !called || f.restores != 0 {
			t.Fatal("did not prefer pin-aware callback")
		}
	}
	f := setup(t)
	decode(t, call(t, f.h, "POST", "/history/restore", map[string]any{"revision": 0, "entry_id": "42", "scope": "state"}), 200)
	if f.restores != 1 {
		t.Fatal("legacy restore callback not called")
	}
	// A legacy callback must never silently ignore a requested pin.
	decode(t, call(t, f.h, "POST", "/history/restore", map[string]any{"revision": 1, "entry_id": "42:a", "scope": "alias_target", "pin": true}), 503)
	if f.restores != 1 {
		t.Fatal("legacy callback ignored pin")
	}
}

func TestHistoryRestoreRejectsInvalidPinsBeforeCallbacks(t *testing.T) {
	for _, tc := range []struct {
		scope string
		pin   any
		code  string
	}{
		{"state", true, "invalid_restore_pin"},
		{"alias_target", "true", "invalid_type"},
		{"alias_target", nil, "invalid_type"},
		{"alias_target", 1, "invalid_type"},
	} {
		f := setup(t)
		out := decode(t, call(t, f.h, "POST", "/history/restore", map[string]any{"revision": 0, "entry_id": "42:a", "scope": tc.scope, "pin": tc.pin}), 422)
		details := out["error"].(map[string]any)["details"].([]any)
		found := false
		for _, value := range details {
			d := value.(map[string]any)
			if d["path"] == "pin" && d["code"] == tc.code {
				found = true
			}
		}
		if !found || f.restores != 0 {
			t.Fatal(out)
		}
	}
}

func TestLegacyAliasSettingHistoryUpgradesPersistedGenericSummaries(t *testing.T) {
	f := setup(t)
	before := policy.DefaultState()
	before.Aliases = []policy.Alias{{ID: "a", Name: "sol", Mode: "pinned"}}
	after := before
	after.Aliases = append([]policy.Alias{}, before.Aliases...)
	after.Aliases[0].Mode = "auto"
	f.h.config.History = func(int, string) any {
		return []any{map[string]any{"id": "1", "actor": "user", "kind": "state", "before": before, "after": after, "summary_detail": policy.NewText("alias_changed", "Changed alias sol", map[string]any{"alias": "sol"})}}
	}
	out := decode(t, call(t, f.h, "GET", "/history", nil), 200)
	detail := out["entries"].([]any)[0].(map[string]any)["summary_detail"].(map[string]any)
	fields := detail["params"].(map[string]any)["fields"]
	if !reflect.DeepEqual(fields, []any{map[string]any{"field": "mode", "from": "pinned", "to": "auto"}}) {
		t.Fatal(detail)
	}
}
