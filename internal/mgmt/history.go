package mgmt

import (
	"encoding/json"
	"fmt"
	"github.com/suk-6/cpa-model-dock/internal/policy"
)

// historyDetail supports legacy callback rows without persisted structured text.
func historyDetail(row map[string]any) {
	if detail, ok := row["summary_detail"].(map[string]any); ok && detail["code"] != "" {
		if detail["params"] == nil {
			detail["params"] = map[string]any{}
		}
		// Older persisted alias settings summaries lack their field diff. Rebuild
		// them from snapshots while preserving explicit restore intent.
		if !historyNeedsFieldDiff(detail) {
			return
		}
	}
	summary, _ := row["summary"].(string)
	actor, _ := row["actor"].(string)
	old, oldOK := row["before"].(map[string]any)
	next, nextOK := row["after"].(map[string]any)
	var detail policy.Text
	if oldOK && nextOK {
		if id, ok := next["alias_id"].(string); ok {
			from, _ := old["target"].(string)
			to, _ := next["target"].(string)
			before := policy.State{Aliases: []policy.Alias{{ID: id, Name: id, Target: from}}}
			after := policy.State{Aliases: []policy.Alias{{ID: id, Name: id, Target: to}}}
			detail = policy.HistorySummary(before, after, actor, summary)
		} else {
			var before, after policy.State
			b, e1 := json.Marshal(old)
			a, e2 := json.Marshal(next)
			if e1 == nil && e2 == nil && json.Unmarshal(b, &before) == nil && json.Unmarshal(a, &after) == nil {
				detail = policy.HistorySummary(before, after, actor, summary)
			}
		}
	}
	if detail.Code == "" {
		if summary == "" {
			summary = fmt.Sprintf("History entry %v", row["id"])
		}
		detail = policy.NewText("history_legacy", summary, map[string]any{"summary": summary})
	}
	row["summary_detail"] = detail
	row["summary"] = detail.Message
}

func historyNeedsFieldDiff(detail map[string]any) bool {
	if detail["code"] == "alias_changed" {
		params, _ := detail["params"].(map[string]any)
		return params["fields"] == nil
	}
	params, _ := detail["params"].(map[string]any)
	changes, _ := params["changes"].([]any)
	for _, value := range changes {
		if child, ok := value.(map[string]any); ok && historyNeedsFieldDiff(child) {
			return true
		}
	}
	return false
}
