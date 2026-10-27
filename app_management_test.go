package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/suk-6/cpa-model-dock/internal/autoupdate"
	"github.com/suk-6/cpa-model-dock/internal/mgmt"
	"github.com/suk-6/cpa-model-dock/internal/policy"
)

func TestManagementRefreshCheckAndRestorePolicyRevisions(t *testing.T) {
	a, err := newApplication(t.TempDir(), fakeHost())
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	a.refresh(context.Background())
	initial := autoState()
	initial.Aliases[1].Mode = "auto"
	initial.Aliases[1].Auto = initial.Aliases[0].Auto
	saved, err := seedApplication(a, initial)
	if err != nil {
		t.Fatal(err)
	}
	h := a.management(7, true)
	call := func(path string, body any, status int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		r := h.Handle(context.Background(), pluginapi.ManagementRequest{Method: "POST", Path: mgmt.BasePath + path, Body: raw})
		if r.StatusCode != status {
			t.Fatalf("%s: %d %s", path, r.StatusCode, r.Body)
		}
		var out map[string]any
		if err := json.Unmarshal(r.Body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := a.current.Load().StateCopy()
	call("/catalog/refresh", map[string]any{"revision": -123}, 200)
	if !reflect.DeepEqual(a.current.Load().StateCopy(), before) || len(a.store.History(100, "")) != 1 {
		t.Fatal("refresh changed policy/history")
	}
	call("/aliases/check", map[string]any{"dry_run": true, "revision": "ignored"}, 200)
	if len(a.status) != 0 || !reflect.DeepEqual(before, a.current.Load().StateCopy()) {
		t.Fatal("dry-run persisted status or targets")
	}
	applied := call("/aliases/check", map[string]any{"revision": saved.Revision}, 200)
	if applied["revision"] != float64(2) || len(applied["results"].([]any)) != 2 {
		t.Fatal(applied)
	}
	rows := a.managementHistory(100, "").([]any)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	for _, row := range rows[:2] {
		obj := row.(map[string]any)
		detail := obj["summary_detail"].(policy.Text)
		if detail.Code != "alias_target_auto_updated" || detail.Params["from"] != "gpt-6.1-sol" || detail.Params["to"] != "gpt-6.2-sol" || obj["summary"] != detail.Message {
			t.Fatal(obj)
		}
		if len(obj["before"].(map[string]any)) != 2 || len(obj["after"].(map[string]any)) != 2 {
			t.Fatal(obj)
		}
	}
	unchanged := call("/aliases/check", map[string]any{"revision": 2}, 200)
	if unchanged["revision"] != float64(2) || len(a.store.History(100, "")) != 2 {
		t.Fatal("unchanged check bumped revision/history")
	}
	first := rows[0].(map[string]any)
	id := first["before"].(map[string]any)["alias_id"].(string)
	a.status[id] = autoupdate.Status{Reason: "test"}
	restored := call("/history/restore", map[string]any{"revision": 2, "entry_id": first["id"], "scope": "alias_target"}, 200)
	restoreRow := a.managementHistory(100, "").([]any)[0].(map[string]any)
	restoreDetail := restoreRow["summary_detail"].(policy.Text)
	if restoreDetail.Code != "alias_target_restored" || restoreDetail.Params["alias_id"] != id || restoreDetail.Params["target"] != "gpt-6.1-sol" {
		t.Fatal(restoreRow)
	}
	if restored["revision"] != float64(3) {
		t.Fatal(restored)
	}
	for _, alias := range a.current.Load().StateCopy().Aliases {
		want := "gpt-6.2-sol"
		if alias.ID == id {
			want = "gpt-6.1-sol"
		}
		if alias.Target != want {
			t.Fatal(alias)
		}
	}
	if _, exists := a.status[id]; exists {
		t.Fatal("restored alias status remained cached")
	}
	if len(a.status) != 1 {
		t.Fatal("restore removed unrelated runtime status")
	}
	call("/history/restore", map[string]any{"revision": 3, "entry_id": first["id"], "scope": "state"}, 422)
	// The original state save's before snapshot is the empty initial policy.
	stateEntry := rows[2].(map[string]any)
	restored = call("/history/restore", map[string]any{"revision": 3, "entry_id": stateEntry["id"], "scope": "state"}, 200)
	if len(restored["aliases"].([]any)) != 0 || restored["revision"] != float64(4) || len(a.status) != 0 {
		t.Fatal(restored)
	}
	call("/history/restore", map[string]any{"revision": 4, "entry_id": first["id"], "scope": "alias_target"}, 422)
	if a.current.Load().StateCopy().Revision != 4 {
		t.Fatal("failed restore changed policy")
	}
}
