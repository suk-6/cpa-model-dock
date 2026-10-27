package hooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var errPayload = errors.New("invalid protocol payload")

var changedEntityHeaders = []string{"Content-Length", "ETag", "Content-MD5", "Digest"}

func rewriteRequest(body []byte, model string) ([]byte, error) {
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, errPayload
	}
	if _, exists := payload["model"]; !exists {
		return body, nil
	}
	payload["model"], _ = json.Marshal(model)
	return json.Marshal(payload)
}

func rewriteModel(body []byte, format, alias string) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errPayload
	}
	if trimmed[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(trimmed, &items) != nil {
			return nil, errPayload
		}
		for i, item := range items {
			var err error
			items[i], err = rewriteModel(item, format, alias)
			if err != nil {
				return nil, err
			}
		}
		return json.Marshal(items)
	}
	var payload map[string]json.RawMessage
	if json.Unmarshal(body, &payload) != nil || payload == nil {
		return nil, errPayload
	}
	encoded, _ := json.Marshal(alias)
	changed := false
	replace := func(object map[string]json.RawMessage, field string) bool {
		raw, ok := object[field]
		if !ok {
			return false
		}
		var old string
		if json.Unmarshal(raw, &old) != nil {
			return false
		}
		object[field] = encoded
		return true
	}
	if strings.Contains(format, "gemini") {
		changed = replace(payload, "modelVersion")
	} else {
		changed = replace(payload, "model")
		container := "response"
		if strings.Contains(format, "claude") || format == "anthropic" {
			container = "message"
		}
		if raw, exists := payload[container]; exists {
			var nested map[string]json.RawMessage
			if json.Unmarshal(raw, &nested) == nil && nested != nil && replace(nested, "model") {
				payload[container], _ = json.Marshal(nested)
				changed = true
			}
		}
	}
	if !changed {
		return body, nil
	}
	return json.Marshal(payload)
}

func (h *Hooks) Response(req pluginapi.ResponseInterceptRequest) (out pluginapi.ResponseInterceptResponse) {
	catalog := req.Model == "" && req.RequestedModel == ""
	defer func() {
		if recover() != nil {
			body := errorBody(req.SourceFormat, unavailable())
			if catalog {
				body = emptyCatalog(req.SourceFormat, req.Body)
			}
			out = pluginapi.ResponseInterceptResponse{Body: body, ClearHeaders: append([]string(nil), changedEntityHeaders...)}
		}
	}()
	if catalog {
		out.Body = filterCatalog(h.latest(), req.SourceFormat, req.Body)
		out.ClearHeaders = append([]string(nil), changedEntityHeaders...)
		return out
	}
	p := h.pin(req.RequestID, requestedName(req.RequestedModel, req.Model), req.Model, false)
	if !sameRequest(p, requestedName(req.RequestedModel, req.Model)) || !p.decision.Allowed {
		out.Body = errorBody(req.SourceFormat, p.decision)
	} else if p.rewrite {
		body, err := rewriteModel(req.Body, req.SourceFormat, p.decision.Alias.Name)
		if err != nil {
			out.Body = errorBody(req.SourceFormat, unavailable())
		} else {
			out.Body = body
		}
	}
	if len(out.Body) > 0 {
		out.ClearHeaders = append([]string(nil), changedEntityHeaders...)
	}
	return out
}

// filterCatalog works from the received protocol catalog, not from fabricated
// metadata. Alias entries are independent clones of their target's actual entry.
func filterCatalog(s *policy.Snapshot, format string, body []byte) []byte {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return emptyCatalog(format, body)
	}
	field := "data"
	if _, exists := envelope["models"]; exists {
		field = "models"
	}
	var entries []map[string]json.RawMessage
	if raw, ok := envelope[field]; !ok || json.Unmarshal(raw, &entries) != nil {
		return emptyCatalog(format, body)
	}
	result := make([]map[string]json.RawMessage, 0, len(entries))
	claude := strings.Contains(format, "claude") || format == "anthropic"
	gemini := strings.Contains(format, "gemini")
	key := "id"
	if field == "models" {
		key = "slug"
		if gemini {
			key = "name"
		}
	}
	canonical := func(entry map[string]json.RawMessage) string {
		name := stringField(entry, key)
		if claude {
			name = resolveClaudeID(name)
		}
		return name
	}
	if s != nil && s.Available() {
		seen := map[string]bool{}
		for _, entry := range entries {
			name := canonical(entry)
			if name == "" {
				continue
			}
			// CPA's own entries are evidence of publication. The callback
			// inventory may be empty during startup or omit host-only models;
			// absence there must not add a second allowlist to saved policy.
			// Injected aliases below still require a known routable target.
			if d := s.Decide(name); d.Allowed {
				result = append(result, entry)
				seen[s.Normalize(name)] = true
			}
		}
		for _, alias := range s.StateCopy().Aliases {
			d := s.Decide(alias.Name)
			if !alias.Enabled || !d.Allowed || d.Alias == nil || !s.Routable(d.ResolvedTarget, d.Provider) || seen[s.Normalize(alias.Name)] {
				continue
			}
			for _, target := range entries {
				if s.Normalize(canonical(target)) != s.Normalize(d.ResolvedTarget) {
					continue
				}
				cloned := make(map[string]json.RawMessage, len(target))
				for k, v := range target {
					cloned[k] = bytes.Clone(v)
				}
				name := alias.Name
				if gemini {
					name = "models/" + name
				}
				if claude {
					name = ensureClaudeID(name)
				}
				cloned[key], _ = json.Marshal(name)
				if key == "slug" {
					if _, exists := cloned["id"]; exists {
						cloned["id"], _ = json.Marshal(alias.Name)
					}
				}
				// Grok Shell carries two executable identifiers; both must name
				// the alias, otherwise selecting the row bypasses publication.
				if key == "id" {
					if _, exists := cloned["model"]; exists {
						cloned["model"], _ = json.Marshal(alias.Name)
					}
				}
				result = append(result, cloned)
				seen[s.Normalize(alias.Name)] = true
				break
			}
		}
	}
	envelope[field], _ = json.Marshal(result)
	// CPA's Anthropic envelope has no remaining page once filtering finishes.
	if _, exists := envelope["first_id"]; exists {
		first, last := "", ""
		if len(result) > 0 {
			first, last = stringField(result[0], "id"), stringField(result[len(result)-1], "id")
		}
		envelope["first_id"], _ = json.Marshal(first)
		envelope["last_id"], _ = json.Marshal(last)
		envelope["has_more"] = json.RawMessage("false")
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return emptyCatalog(format, body)
	}
	return encoded
}

func stringField(entry map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(entry[key], &value)
	return value
}

func emptyCatalog(format string, original []byte) []byte {
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(original, &envelope)
	if strings.Contains(format, "gemini") {
		return []byte(`{"models":[]}`)
	}
	if _, ok := envelope["models"]; ok {
		return []byte(`{"models":[]}`)
	}
	if strings.Contains(format, "claude") || format == "anthropic" {
		return []byte(`{"data":[],"first_id":"","last_id":"","has_more":false}`)
	}
	return []byte(`{"object":"list","data":[]}`)
}

// CPA's verified Claude catalog cloak is reversible and is removed by its
// request handler before routing (internal/client/claude/models/models.go).
const claudeCloak = "claude-fable-5-dd-"

func reverseRunes(value string) string {
	runes := []rune(value)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func resolveClaudeID(value string) string {
	if strings.HasPrefix(value, claudeCloak) && len(value) > len(claudeCloak) {
		return reverseRunes(strings.TrimPrefix(value, claudeCloak))
	}
	return value
}

func ensureClaudeID(value string) string {
	if value == "" || strings.HasPrefix(value, "claude-") {
		return value
	}
	return claudeCloak + reverseRunes(value)
}

func streamFailure(format string) []byte {
	body := errorBody(format, unavailable())
	if strings.Contains(format, "claude") || format == "anthropic" {
		return append(append([]byte("event: error\ndata: "), body...), []byte("\n\n")...)
	}
	return append(append([]byte("data: "), body...), []byte("\n\n")...)
}

func streamHeaders() http.Header { return http.Header{"Cache-Control": {"no-store"}} }
