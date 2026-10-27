package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/cpa-model-dock/cpa-model-dock/internal/version"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/cpa-model-dock/cpa-model-dock/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type publicPreview struct {
	PublicModels  []string `json:"public_models"`
	PublicAliases []string `json:"public_aliases"`
}

func public(s *policy.Snapshot) publicPreview {
	p := publicPreview{PublicModels: []string{}, PublicAliases: []string{}}
	seen := map[string]bool{}
	for _, m := range s.CatalogCopy().Models {
		if m.Routable && s.Decide(m.ID).Allowed && !seen[m.ID] {
			p.PublicModels = append(p.PublicModels, m.ID)
			seen[m.ID] = true
		}
	}
	for _, a := range s.StateCopy().Aliases {
		if a.Enabled && s.Decide(a.Name).Allowed {
			p.PublicAliases = append(p.PublicAliases, a.Name)
		}
	}
	sort.Strings(p.PublicModels)
	sort.Strings(p.PublicAliases)
	return p
}

func (h *Handler) stateView(ctx context.Context, s *policy.Snapshot) any {
	state := s.StateCopy()
	if state.Families == nil {
		state.Families = []policy.Family{}
	}
	if state.Aliases == nil {
		state.Aliases = []policy.Alias{}
	}
	if state.Rules == nil {
		state.Rules = []policy.Rule{}
	}
	return struct {
		policy.State
		BuiltinFamilies []policy.Family `json:"builtin_families"`
		AliasStatus     any             `json:"alias_status"`
	}{state, policy.BuiltinFamilies(), h.aliasStatus(ctx, s)}
}

func (h *Handler) aliasStatus(ctx context.Context, s *policy.Snapshot) any {
	statuses := map[string]any{}
	for _, alias := range s.StateCopy().Aliases {
		routable := false
		for _, model := range s.CatalogCopy().Models {
			if s.Normalize(model.ID) != s.Normalize(alias.Target) || !model.Routable {
				continue
			}
			if alias.Provider == "" {
				routable = true
			}
			for _, provider := range model.Providers {
				if strings.EqualFold(alias.Provider, provider) {
					routable = true
				}
			}
		}
		statuses[alias.ID] = map[string]any{
			"current_target": alias.Target, "target_routable": routable,
			"candidates": []any{}, "last_result": "kept", "reason": reasonView("not_checked", "Alias has not been evaluated"),
			"last_checked_at": nil,
		}
	}
	if len(statuses) != 0 && h.config.AliasStatus != nil {
		values := jsonObject(h.config.AliasStatus())
		for id, base := range statuses {
			if value, ok := values[id].(map[string]any); ok {
				status := statusView(value)
				status["current_target"] = base.(map[string]any)["current_target"]
				status["target_routable"] = base.(map[string]any)["target_routable"]
				statuses[id] = status
			}
		}
	}
	return statuses
}

func jsonObject(value any) map[string]any {
	var obj map[string]any
	if b, err := json.Marshal(value); err == nil {
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		_ = decoder.Decode(&obj)
	}
	return obj
}

func (h *Handler) overview(ctx context.Context, s *policy.Snapshot) any {
	state, catalog := s.StateCopy(), s.CatalogCopy()
	p := public(s)
	mappings := []any{}
	warnings := []any{reasonView("priority_unverified", "Verify plugin priority relative to other installed plugins on the host")}
	if !catalog.OK {
		warnings = append(warnings, reasonView("catalog_unavailable", "Could not refresh models; refresh again and check the provider connection"))
	}
	if !s.Available() {
		warnings = append(warnings, reasonView("policy_unavailable", "Policy is unavailable; repair the saved settings to allow model requests"))
	}
	covered := []any{
		reasonView("http_model_requests_covered", "Ordinary HTTP model requests, including streaming, are covered by model policy"),
		reasonView("model_lists_covered", "Model list responses are filtered by model policy"),
		reasonView("responses_websocket_initial_covered", "The initial Responses WebSocket request is covered by model policy"),
	}
	limitations := []any{
		reasonView("realtime_proxy_required", "Realtime and live requests need reverse-proxy controls. Valid API keys and realtime client secrets can bypass this policy on standard CPA"),
		reasonView("websocket_messages_proxy_required", "Ongoing WebSocket messages require reverse-proxy controls"),
		reasonView("model_detail_proxy_required", "Single-model detail endpoints require reverse-proxy controls"),
		reasonView("admin_api_tool_proxy_required", "The management API tool requires reverse-proxy controls"),
	}
	for _, alias := range state.Aliases {
		if alias.Enabled {
			if !s.Routable(alias.Target, alias.Provider) {
				warnings = append(warnings, reasonView("alias_target_unavailable", "Alias "+alias.Name+" target "+alias.Target+" is unavailable; choose an available model", map[string]any{"alias_id": alias.ID, "alias": alias.Name, "target": alias.Target}))
			}
			mappings = append(mappings, map[string]any{"alias": alias.Name, "target": alias.Target})
		}
	}
	recent := []any{}
	if h.config.History != nil {
		entries := historyEntries(h.config.History(store.HistoryLimit, ""))
		for _, entry := range entries {
			row, ok := entry.(map[string]any)
			if ok && row["actor"] == "auto" && row["kind"] == "alias_target" {
				recent = append(recent, entry)
				if len(recent) == 5 {
					break
				}
			}
		}
	}
	return map[string]any{
		"plugin_version": h.config.Version, "host_schema": h.config.HostSchema,
		"policy_enabled": s.Available(), "whitelist_enabled": state.Settings.WhitelistEnabled,
		"counts":          map[string]int{"public_models": len(p.PublicModels), "public_aliases": len(p.PublicAliases)},
		"active_mappings": mappings, "recent_auto_updates": recent,
		"catalog":  map[string]any{"last_refresh_at": catalog.RefreshedAt, "ok": catalog.OK, "error": nullableError(catalog.Error), "error_detail": catalogError(catalog)},
		"guard":    map[string]any{"enabled": h.config.Guard, "verified": false, "limitations": limitations, "covered": covered},
		"warnings": warnings,
	}
}

func catalogView(s *policy.Snapshot, catalog policy.Catalog) any {
	models := []any{}
	for _, model := range catalog.Models {
		d := s.Decide(model.ID)
		providers := append([]string{}, model.Providers...)
		models = append(models, map[string]any{
			"id": model.ID, "providers": providers, "credential_count": model.CredentialCount,
			"routable": model.Routable, "public": model.Routable && d.Allowed, "reason": catalogReason(model, d),
		})
	}
	return map[string]any{"refreshed_at": catalog.RefreshedAt, "ok": catalog.OK, "error": nullableError(catalog.Error), "error_detail": catalogError(catalog), "models": models}
}

func (h *Handler) validate(body object) pluginapi.ManagementResponse {
	s := h.snapshot()
	state, details := fullState(body, s.StateCopy())
	if len(details) == 0 {
		details = ValidateState(state, s.CatalogCopy())
	}
	if details == nil {
		details = []policy.Detail{}
	}
	preview := publicPreview{PublicModels: []string{}, PublicAliases: []string{}}
	if len(details) == 0 {
		preview = public(policy.New(state, s.CatalogCopy(), true))
	}
	return response(http.StatusOK, map[string]any{"valid": len(details) == 0, "details": detailViews(details), "preview": preview})
}

func (h *Handler) preview(body object) pluginapi.ManagementResponse {
	s := h.snapshot()
	state, details := draftState(body, s.StateCopy())
	if len(details) == 0 {
		details = assignIDs(&state)
	}
	if len(details) == 0 {
		details = ValidateState(state, s.CatalogCopy())
	}
	if len(details) != 0 {
		return invalid(details)
	}
	draft := policy.New(state, s.CatalogCopy(), true)
	p := public(draft)
	rules := []any{}
	for _, rule := range state.Rules {
		all := []string{}
		if rule.Enabled {
			if rule.Kind == "alias" {
				for _, alias := range state.Aliases {
					if draft.Normalize(alias.Name) == draft.Normalize(rule.Value) {
						all = append(all, alias.Name)
					}
				}
			} else {
				for _, model := range s.CatalogCopy().Models {
					if ruleMatch(draft, rule, model) {
						all = append(all, model.ID)
					}
				}
			}
		}
		sort.Strings(all)
		rules = append(rules, map[string]any{"id": rule.ID, "matches": all})
	}
	return response(http.StatusOK, map[string]any{"rules": rules, "public_models": p.PublicModels, "public_aliases": p.PublicAliases})
}

func (h *Handler) explain(body object) pluginapi.ManagementResponse {
	var model string
	details := field(body, "model", &model)
	if strings.TrimSpace(model) == "" {
		details = append(details, detail("model", "required_model", "A model name is required"))
	}
	s := h.snapshot()
	if raw, ok := body["draft"]; ok {
		draft, errs := decodeObject(raw)
		if len(errs) != 0 {
			details = append(details, detail("draft", "invalid_type", "Draft must be an object", map[string]any{"expected": "object"}))
		} else {
			state, errs := draftState(draft, s.StateCopy())
			if len(errs) == 0 {
				errs = assignIDs(&state)
			}
			details = append(details, errs...)
			if len(errs) == 0 {
				details = append(details, ValidateState(state, s.CatalogCopy())...)
			}
			if len(details) == 0 {
				s = policy.New(state, s.CatalogCopy(), true)
			}
		}
	}
	if len(details) != 0 {
		return invalid(details)
	}
	d := s.Decide(model)
	steps := []any{}
	for _, code := range d.Steps {
		message, params := d.Reason, map[string]any{}
		switch code {
		case "normalized":
			message = "Matched as " + s.Normalize(model)
			params["model"] = s.Normalize(model)
		case "resolved_model":
			message = "Evaluating resolved model " + s.Normalize(model)
			params["model"] = s.Normalize(model)
		default:
			params = decisionParams(d)
		}
		steps = append(steps, reasonView(code, message, params))
	}
	view := map[string]any{"allowed": d.Allowed, "status": d.Status, "code": d.Code, "reason": reasonView(d.Code, d.Reason, decisionParams(d)), "steps": steps}
	if d.ResolvedTarget != "" {
		view["resolved_target"] = d.ResolvedTarget
	}
	return response(http.StatusOK, view)
}

func (h *Handler) check(ctx context.Context, body object, dryRun bool, s *policy.Snapshot) pluginapi.ManagementResponse {
	var aliasID string
	if details := field(body, "alias_id", &aliasID); len(details) != 0 {
		return invalid(details)
	}
	if aliasID != "" {
		found := false
		for _, alias := range s.StateCopy().Aliases {
			found = found || alias.ID == aliasID
		}
		if !found {
			return failure(http.StatusNotFound, "alias_not_found", "Alias not found")
		}
	}
	if h.config.Check == nil && h.config.CheckRevision == nil {
		return unavailable()
	}
	result, err := h.evaluate(ctx, s.StateCopy().Revision, aliasID, dryRun)
	if err != nil {
		return h.callbackError(err)
	}
	values := jsonObject(result)
	results := []any{}
	for _, alias := range s.StateCopy().Aliases {
		if value, ok := values[alias.ID].(map[string]any); ok {
			status := statusView(value)
			delete(status, "target_routable")
			status["alias_id"] = alias.ID
			results = append(results, status)
		}
	}
	return response(http.StatusOK, map[string]any{"results": results, "revision": h.snapshot().StateCopy().Revision})
}

func (h *Handler) evaluate(ctx context.Context, expected int64, aliasID string, dryRun bool) (any, error) {
	if h.config.CheckRevision != nil {
		return h.config.CheckRevision(ctx, expected, aliasID, dryRun)
	}
	return h.config.Check(ctx, aliasID, dryRun)
}

func historyEntries(value any) []any {
	var entries []any
	if b, err := json.Marshal(value); err == nil {
		decoder := json.NewDecoder(bytes.NewReader(b))
		decoder.UseNumber()
		if decoder.Decode(&entries) != nil {
			entries, _ = jsonObject(value)["entries"].([]any)
		}
	}
	if entries == nil {
		entries = []any{}
	}
	for _, entry := range entries {
		if row, ok := entry.(map[string]any); ok {
			historyDetail(row)
		}
	}
	return entries
}

func (h *Handler) history(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	limit := 100
	if values, ok := req.Query["limit"]; ok {
		if len(values) != 1 {
			return failure(400, "invalid_limit", "Use one integer limit between 1 and 1000")
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > store.HistoryLimit {
			return failure(400, "invalid_limit", "Use one integer limit between 1 and 1000")
		}
		limit = parsed
	}
	if len(req.Query["before"]) > 1 {
		return failure(400, "invalid_cursor", "Use one history cursor")
	}
	entries := []any{}
	if h.config.History != nil {
		entries = historyEntries(h.config.History(store.HistoryLimit, ""))
	}
	if before := req.Query.Get("before"); before != "" {
		index := -1
		for i, entry := range entries {
			if row, ok := entry.(map[string]any); ok && row["id"] == before {
				index = i
				break
			}
		}
		if index < 0 {
			return failure(400, "invalid_cursor", "Unknown history cursor")
		}
		entries = entries[index+1:]
	}
	var next any
	if len(entries) > limit {
		entries = entries[:limit]
		next = entries[len(entries)-1].(map[string]any)["id"]
	}
	return response(http.StatusOK, map[string]any{"entries": entries, "next_before": next})
}

func reasonView(code, message string, params ...map[string]any) map[string]any {
	t := policy.NewText(code, message, params...)
	return map[string]any{"code": t.Code, "params": t.Params, "message": t.Message}
}
func decisionParams(d policy.Decision) map[string]any {
	p := map[string]any{}
	if d.RuleID != "" {
		p["rule_id"] = d.RuleID
	}
	if d.Alias != nil {
		p["alias"] = d.Alias.Name
		p["target"] = d.ResolvedTarget
	}
	return p
}
func catalogError(c policy.Catalog) any {
	if c.OK {
		return nil
	}
	return reasonView("catalog_unavailable", "Could not refresh models; previously available models are retained")
}
func nullableError(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func catalogReason(model policy.Model, d policy.Decision) any {
	if !model.Routable {
		return reasonView("not_routable", "No connected provider offers this model")
	}
	return reasonView(d.Code, d.Reason, decisionParams(d))
}

// statusView is the wire boundary: internal evaluator structs are never exposed.
func statusView(value map[string]any) map[string]any {
	code, _ := value["reason"].(string)
	message, _ := value["message"].(string)
	params := map[string]any{}
	if reason, ok := value["reason"].(map[string]any); ok {
		code, _ = reason["code"].(string)
		message, _ = reason["message"].(string)
		if p, ok := reason["params"].(map[string]any); ok {
			params = p
		}
	}
	switch code {
	case "newer_candidate":
		code = "newer_version"
	case "no_newer_candidate":
		code = "up_to_date"
	case "health_check_unavailable":
		code = "health_check_unverified"
	}
	if code == "" {
		code = "not_checked"
	}
	if code == "newer_version" {
		params["from"] = value["previous_target"]
		params["to"] = value["current_target"]
	}
	if message == "" {
		message = code
	}
	result := value["last_result"]
	if result == nil {
		result = "kept"
	}
	if code == "disabled" || code == "pinned" {
		result = "kept"
	}
	candidates := []any{}
	if rows, ok := value["candidates"].([]any); ok {
		for _, row := range rows {
			if candidate, ok := row.(map[string]any); ok {
				code, _ := candidate["reason"].(string)
				if r, ok := candidate["reason"].(map[string]any); ok {
					code, _ = r["code"].(string)
				}
				if code == "prerelease_excluded" {
					continue
				}
				if code == "" {
					code = "candidate"
				}
				if code == "chosen" {
					code = "selected"
				}
				if code == "unroutable" || code == "provider_mismatch" {
					code = "not_routable"
				}
				if candidate["chosen"] == true {
					code = "selected"
				}
				candidates = append(candidates, map[string]any{"id": candidate["id"], "version": candidate["version"], "routable": candidate["routable"], "chosen": candidate["chosen"], "reason": reasonView(code, candidateMessage(code))})
			}
		}
	}
	return map[string]any{"current_target": value["current_target"], "candidates": candidates, "last_checked_at": value["last_checked_at"], "last_result": result, "reason": reasonView(code, message, params), "target_routable": value["target_routable"]}
}

func ruleMatch(s *policy.Snapshot, rule policy.Rule, model policy.Model) bool {
	if rule.Kind == "family" {
		family, ok := s.Family(rule.Value)
		if !ok {
			return false
		}
		template, err := version.Compile(family.Template, family.AllowDated)
		if err != nil {
			return false
		}
		value, ok := template.Parse(s.Normalize(model.ID))
		if !ok || value.Prerelease != "" {
			return false
		}
		if family.Provider != "" {
			for _, provider := range model.Providers {
				if strings.EqualFold(provider, family.Provider) {
					return true
				}
			}
			return false
		}
		return true
	}
	// Remove alias hiding and routability from the individual rule match list.
	state := s.StateCopy()
	state.Aliases = nil
	state.Settings.WhitelistEnabled = true
	state.Rules = []policy.Rule{rule}
	return policy.New(state, s.CatalogCopy(), true).Decide(model.ID).Allowed
}

func candidateMessage(code string) string {
	switch code {
	case "candidate":
		return "Eligible family candidate"
	case "selected":
		return "Selected as the newer available model"
	case "not_routable":
		return "The required provider does not offer this model"
	case "alias_conflict":
		return "Candidate conflicts with a configured alias name"
	default:
		return "Candidate is unavailable"
	}
}
