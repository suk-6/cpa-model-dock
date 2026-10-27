package mgmt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

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
			"candidates": []any{}, "last_result": "kept", "reason": "not_checked",
			"last_checked_at": time.Time{}, "message": "Alias has not been evaluated",
		}
	}
	if len(statuses) == 0 {
		return statuses
	}
	var runtime any
	if h.config.AliasStatus != nil {
		runtime = h.config.AliasStatus()
	} else if (h.config.Check != nil || h.config.CheckRevision != nil) && len(statuses) != 0 {
		result, err := h.evaluate(ctx, s.StateCopy().Revision, "", true)
		if err == nil {
			runtime = result
		}
	}
	if runtime != nil {
		values := jsonObject(runtime)
		for _, key := range []string{"alias_status", "statuses", "results"} {
			if nested, ok := values[key].(map[string]any); ok {
				values = nested
				break
			}
		}
		for id := range statuses {
			if value, ok := values[id].(map[string]any); ok {
				status := statuses[id].(map[string]any)
				for key, value := range value {
					// Cached evaluation can predate a state save or catalog refresh.
					if key != "current_target" && key != "target_routable" && value != nil {
						status[key] = value
					}
				}
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
	warnings := []string{
		"Responses WebSocket continuation frames, Gemini model detail and management api-call are not intercepted; use reverse-proxy controls",
		"Valid built-in API keys and realtime client secrets can bypass the nonexclusive frontend guard on stock CPA; restrict realtime/live at a reverse proxy",
		"Plugin priority relative to other installed plugins must be verified on the host",
	}
	if !h.config.Guard {
		warnings = append(warnings, "Realtime and live endpoint guard is disabled")
	}
	if !s.Available() {
		warnings = append(warnings, "Policy is unavailable; model requests are denied")
	}
	for _, alias := range state.Aliases {
		if alias.Enabled && s.Decide(alias.Name).Allowed {
			mappings = append(mappings, map[string]any{"id": alias.ID, "name": alias.Name, "target": alias.Target, "provider": alias.Provider})
		}
	}
	recent := []any{}
	if h.config.History != nil {
		entries := historyEntries(h.config.History(100, ""))
		for _, entry := range entries {
			row, ok := entry.(map[string]any)
			if ok && row["actor"] == "auto" {
				recent = append(recent, entry)
				if len(recent) == 5 {
					break
				}
			}
		}
	}
	return map[string]any{
		"version": h.config.Version, "host_schema": h.config.HostSchema,
		"policy_enabled": s.Available(), "whitelist_enabled": state.Settings.WhitelistEnabled,
		"counts":          map[string]int{"public_models": len(p.PublicModels), "public_aliases": len(p.PublicAliases)},
		"active_mappings": mappings, "recent_auto_updates": recent,
		"catalog":  map[string]any{"last_refresh_at": catalog.RefreshedAt, "ok": catalog.OK, "error": catalog.Error},
		"guard":    map[string]any{"enabled": h.config.Guard, "active": h.config.Guard && state.Settings.WhitelistEnabled, "verified": false, "mandatory_gate": false},
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
			"routable": model.Routable, "public": model.Routable && d.Allowed, "reason": d.Reason,
		})
	}
	return map[string]any{"refreshed_at": catalog.RefreshedAt, "ok": catalog.OK, "error": catalog.Error, "models": models}
}

func (h *Handler) validate(body object) pluginapi.ManagementResponse {
	s := h.snapshot()
	state, details := fullState(body, s.StateCopy())
	if len(details) == 0 {
		details = policy.Validate(state, s.CatalogCopy())
	}
	if details == nil {
		details = []policy.Detail{}
	}
	preview := publicPreview{PublicModels: []string{}, PublicAliases: []string{}}
	if len(details) == 0 {
		preview = public(policy.New(state, s.CatalogCopy(), true))
	}
	return response(http.StatusOK, map[string]any{"valid": len(details) == 0, "details": details, "preview": preview})
}

func (h *Handler) preview(body object) pluginapi.ManagementResponse {
	s := h.snapshot()
	state, details := draftState(body, s.StateCopy())
	if len(details) == 0 {
		details = assignIDs(&state, s.StateCopy(), false)
	}
	if len(details) == 0 {
		details = policy.Validate(state, s.CatalogCopy())
	}
	if len(details) != 0 {
		return invalid(details)
	}
	draft := policy.New(state, s.CatalogCopy(), true)
	p := public(draft)
	rules := []any{}
	for _, rule := range state.Rules {
		one := draft.StateCopy()
		one.Settings.WhitelistEnabled = true
		one.Rules = []policy.Rule{rule}
		engine := policy.New(one, s.CatalogCopy(), true)
		matches := public(engine)
		all := append(matches.PublicModels, matches.PublicAliases...)
		sort.Strings(all)
		rules = append(rules, map[string]any{"id": rule.ID, "matches": all})
	}
	return response(http.StatusOK, map[string]any{"rules": rules, "public_models": p.PublicModels, "public_aliases": p.PublicAliases})
}

func (h *Handler) explain(body object) pluginapi.ManagementResponse {
	var model string
	details := field(body, "model", &model)
	if strings.TrimSpace(model) == "" {
		details = append(details, detail("model", "required", "A model name is required"))
	}
	s := h.snapshot()
	if raw, ok := body["draft"]; ok {
		draft, errs := decodeObject(raw)
		if len(errs) != 0 {
			details = append(details, detail("draft", "invalid_type", "Draft must be an object"))
		} else {
			state, errs := draftState(draft, s.StateCopy())
			if len(errs) == 0 {
				errs = assignIDs(&state, s.StateCopy(), false)
			}
			details = append(details, errs...)
			if len(errs) == 0 {
				details = append(details, policy.Validate(state, s.CatalogCopy())...)
			}
			if len(details) == 0 {
				s = policy.New(state, s.CatalogCopy(), true)
			}
		}
	}
	if len(details) != 0 {
		return invalid(details)
	}
	return response(http.StatusOK, s.Decide(model))
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
	return response(http.StatusOK, result)
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
	return entries
}

func (h *Handler) history(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	limit := 100
	if values, ok := req.Query["limit"]; ok {
		if len(values) != 1 {
			return invalid([]policy.Detail{detail("limit", "invalid_limit", "Limit must be a positive integer")})
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > store.HistoryLimit {
			return invalid([]policy.Detail{detail("limit", "invalid_limit", "Limit must be between 1 and 1000")})
		}
		limit = parsed
	}
	if h.config.History == nil {
		return response(http.StatusOK, map[string]any{"entries": []any{}})
	}
	return response(http.StatusOK, map[string]any{"entries": historyEntries(h.config.History(limit, req.Query.Get("before")))})
}
