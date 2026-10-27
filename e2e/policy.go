package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type settings struct {
	Whitelist bool `json:"whitelist_enabled"`
	Rewrite   bool `json:"rewrite_response_model"`
	Refresh   int  `json:"refresh_interval_seconds"`
}
type alias struct {
	ID       string     `json:"id,omitempty"`
	Name     string     `json:"name"`
	Enabled  bool       `json:"enabled"`
	Mode     string     `json:"mode"`
	Target   string     `json:"target"`
	Provider string     `json:"provider"`
	Expose   bool       `json:"expose_original"`
	Rewrite  *bool      `json:"rewrite_response_model"`
	Auto     autoPolicy `json:"auto"`
}
type autoPolicy struct {
	Family     string `json:"family"`
	Prerelease bool   `json:"include_prerelease"`
	Health     bool   `json:"health_check"`
}
type rule struct {
	ID      string `json:"id,omitempty"`
	Enabled bool   `json:"enabled"`
	Kind    string `json:"kind"`
	Value   string `json:"value"`
}

// Deliberately omit runtime-only status/builtin_families/schema/updated_at on PUT.
type policy struct {
	Revision int64             `json:"revision"`
	Settings settings          `json:"settings"`
	Families []json.RawMessage `json:"families"`
	Aliases  []alias           `json:"aliases"`
	Rules    []rule            `json:"rules"`
}

var pluginScenarios = []string{
	"plugin registration and resources", "pinned alias and response model rewrite", "alias-only whitelist listing", "blocked direct/prefix/native/thinking/other models", "blocked Claude notation", "blocked Gemini notation", "frontend-auth realtime/live guard observation", "Gemini hidden detail observation", "auto-update numeric version selection", "removed target keeps mapping", "stale revision rejected", "invalid state preserves policy", "restart restores state and history",
}

func (h *harness) state() policy {
	r := h.mgmt("GET", "/state", nil)
	wantStatus(r, 200)
	p := decode[policy](r)
	require(p.Families != nil && p.Aliases != nil && p.Rules != nil, "state collections must be arrays: %s", short(r.body))
	return p
}
func (h *harness) save(p policy) policy {
	r := h.mgmt("PUT", "/state", p)
	wantStatus(r, 200)
	out := h.state()
	require(out.Revision > p.Revision, "revision did not advance: %d -> %d", p.Revision, out.Revision)
	return out
}
func (h *harness) action(path string, fields map[string]any) response {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["revision"] = h.state().Revision
	r := h.mgmt("POST", path, fields)
	wantStatus(r, 200)
	return r
}
func (h *harness) dockAlias(p policy) alias {
	for _, a := range p.Aliases {
		if a.Name == "gpt-sol" {
			return a
		}
	}
	panic("state missing gpt-sol alias")
}
func (h *harness) setAlias(f func(*alias)) policy {
	p := h.state()
	found := false
	for i := range p.Aliases {
		if p.Aliases[i].Name == "gpt-sol" {
			f(&p.Aliases[i])
			found = true
		}
	}
	require(found, "missing gpt-sol")
	return h.save(p)
}
func (h *harness) denied(path, model string) string {
	before := len(h.upstream.snapshot())
	r := h.infer(path, model, false)
	require(r.status == 403 || r.status == 404, "denial HTTP %d, want 403 or 404: %s", r.status, short(r.body))
	require(len(h.upstream.snapshot()) == before, "denied request reached upstream: %s model=%s", path, model)
	// A generic host 404/unknown-provider error must not masquerade as a plugin denial.
	var v map[string]any
	must(json.Unmarshal(r.body, &v))
	require(policyDenial(r), "denial lacks contract policy code: HTTP %d %s", r.status, short(r.body))
	return fmt.Sprintf("%s HTTP %d; zero upstream calls", model, r.status)
}
func policyDenial(r response) bool {
	for _, s := range []string{"model_not_allowed", "model_not_found", "hidden_by_alias", "not_whitelisted", "alias_not_published"} {
		if bytes.Contains(r.body, []byte(s)) {
			return true
		}
	}
	return false
}
func (h *harness) whitelistPolicy() {
	ids := h.models()
	require(reflect.DeepEqual(ids, []string{"gpt-sol"}), "whitelist models=%v want [gpt-sol]", ids)
	h.denied("/v1/chat/completions", "gpt-6.1-sol")
}
func (h *harness) pluginChecks() {
	ready := h.check(pluginScenarios[0], func() string {
		r := h.http("GET", "/v8/management/plugins", nil, h.managementKey)
		wantStatus(r, 200)
		var list struct {
			Enabled bool `json:"plugins_enabled"`
			Plugins []struct {
				ID         string `json:"id"`
				Registered bool   `json:"registered"`
				Effective  bool   `json:"effective_enabled"`
				Menus      []struct {
					Menu string `json:"menu"`
					Path string `json:"path"`
				} `json:"menus"`
			} `json:"plugins"`
		}
		must(json.Unmarshal(r.body, &list))
		found := false
		for _, p := range list.Plugins {
			if p.ID != "cpa-model-dock" {
				continue
			}
			require(list.Enabled && p.Registered && p.Effective, "plugin not enabled and registered: %s", short(r.body))
			for _, m := range p.Menus {
				if m.Menu == "Model Dock" && m.Path == "/v0/resource/plugins/cpa-model-dock/index.html" {
					found = true
				}
			}
		}
		require(found, "missing Model Dock menu with contract resource path: %s", short(r.body))
		resource := h.http("GET", "/v0/resource/plugins/cpa-model-dock/index.html", nil, "")
		wantStatus(resource, 200)
		require(strings.Contains(strings.ToLower(string(resource.body)), "<html"), "resource is not HTML")
		require(strings.Contains(resource.header.Get("Content-Type"), "text/html"), "resource Content-Type=%s", resource.header.Get("Content-Type"))
		// Plugin management data must never become a public static resource.
		wantStatus(h.http("GET", base+"/state", nil, ""), 401)
		h.action("/catalog/refresh", nil)
		_ = h.state()
		return "registered/effective, Model Dock menu, public HTML, authenticated state"
	})
	if !ready {
		for _, name := range pluginScenarios[1:] {
			h.rows = append(h.rows, row{"SKIP", name, "plugin registration/API prerequisite failed"})
		}
		return
	}
	pinned := h.check(pluginScenarios[1], func() string {
		p := h.state()
		p.Settings = settings{Refresh: 300}
		p.Families = []json.RawMessage{}
		p.Rules = []rule{}
		p.Aliases = []alias{{Name: "gpt-sol", Enabled: true, Mode: "pinned", Target: "gpt-6.1-sol", Expose: false, Auto: autoPolicy{Family: "builtin:gpt-sol"}}}
		p = h.save(p)
		require(h.dockAlias(p).ID != "", "alias ID was not generated by server")
		for _, rewrite := range []bool{false, true} {
			p = h.state()
			p.Settings.Rewrite = rewrite
			h.save(p)
			want := "gpt-6.1-sol"
			if rewrite {
				want = "gpt-sol"
			}
			h.chat("gpt-sol", "gpt-6.1-sol", false, want)
			h.chat("gpt-sol", "gpt-6.1-sol", true, want)
		}
		// Explicit alias override must beat the global option.
		off := false
		h.setAlias(func(a *alias) { a.Rewrite = &off })
		h.chat("gpt-sol", "gpt-6.1-sol", false, "gpt-6.1-sol")
		h.setAlias(func(a *alias) { a.Rewrite = nil })
		return "pinned JSON/SSE route target and rewrite off/on; alias override checked"
	})
	if !pinned {
		for _, name := range pluginScenarios[2:] {
			h.rows = append(h.rows, row{"SKIP", name, "pinned alias prerequisite failed"})
		}
		return
	}
	whitelisted := h.check(pluginScenarios[2], func() string {
		p := h.state()
		p.Settings.Whitelist = true
		p.Settings.Rewrite = true
		p.Rules = []rule{{ID: "r_e2e_alias", Enabled: true, Kind: "alias", Value: "gpt-sol"}}
		h.save(p)
		h.whitelistPolicy()
		h.chat("gpt-sol", "gpt-6.1-sol", false, "gpt-sol")
		h.chat("gpt-sol", "gpt-6.1-sol", true, "gpt-sol")
		return "/v1/models exactly [gpt-sol]; alias JSON/SSE still route"
	})
	if !whitelisted {
		for _, name := range pluginScenarios[3:] {
			h.rows = append(h.rows, row{"SKIP", name, "whitelist prerequisite failed"})
		}
		return
	}
	h.check(pluginScenarios[3], func() string {
		results := []string{}
		for _, m := range []string{"gpt-6.1-sol", "prefix/gpt-6.1-sol", "cpa-native-sol", "gpt-6.1-sol(high)", "other-model-x"} {
			require(h.routable[m], "baseline did not establish route for %s", m)
			results = append(results, h.denied("/v1/chat/completions", m))
		}
		return strings.Join(results, "; ")
	})
	for i, path := range []string{"/v1/messages", "/gemini"} {
		p := path
		h.check(pluginScenarios[4+i], func() string {
			if !h.routable[p] {
				panic(skipped("baseline CPA cannot route this protocol to configured OpenAI-compatible credential"))
			}
			return h.denied(p, "gpt-6.1-sol")
		})
	}
	h.check(pluginScenarios[6], func() string {
		results := []string{}
		before := len(h.upstream.snapshot())
		for _, p := range []string{"GET /v1/realtime", "POST /v1/realtime", "POST /v1/live"} {
			fields := strings.SplitN(p, " ", 2)
			r, err := h.guardRequest(fields[0], fields[1])
			must(err)
			old, ok := h.guardBaseline[p]
			require(ok, "missing whitelist-off guard baseline for %s", p)
			// Generic 400/503 from a native endpoint is not evidence that FrontendAuth denied.
			denied := (r.status == 401 || r.status == 403) && (policyDenial(r) || (old.status != 401 && old.status != 403))
			results = append(results, fmt.Sprintf("%s guard_denied=%v off=%d on=%d body=%s", p, denied, old.status, r.status, short(r.body)))
		}
		require(len(h.upstream.snapshot()) == before, "guard probes unexpectedly invoked fake inference")
		return "OBSERVATION: " + strings.Join(results, "; ")
	})
	h.check(pluginScenarios[7], func() string {
		before := len(h.upstream.snapshot())
		r := h.http("GET", "/v1beta/models/gpt-6.1-sol", nil, h.clientKey)
		require(len(h.upstream.snapshot()) == before, "model detail unexpectedly invoked inference")
		leak := r.status == 200 && bytes.Contains(r.body, []byte("gpt-6.1-sol"))
		return fmt.Sprintf("OBSERVATION: hidden_detail_leaks=%v HTTP %d body=%s (known host gap; not a policy assertion)", leak, r.status, short(r.body))
	})
	autoOK := h.check(pluginScenarios[8], func() string {
		h.setAlias(func(a *alias) { a.Mode = "auto"; a.Auto = autoPolicy{Family: "builtin:gpt-sol"} })
		h.action("/catalog/refresh", nil)
		a := h.dockAlias(h.state())
		h.action("/aliases/check", map[string]any{"alias_id": a.ID, "dry_run": false})
		h.eventually("auto alias -> gpt-6.10-sol", func() bool { return h.dockAlias(h.state()).Target == "gpt-6.10-sol" })
		h.chat("gpt-sol", "gpt-6.10-sol", false, "gpt-sol")
		h.chat("gpt-sol", "gpt-6.10-sol", true, "gpt-sol")
		return "6.10 beats 6.9; mini and preview excluded; JSON/SSE reach 6.10"
	})
	if autoOK {
		h.check(pluginScenarios[9], func() string {
			h.removeLatest()
			h.waitConfigAbsent("gpt-6.10-sol")
			h.action("/catalog/refresh", nil)
			h.eventually("catalog marks removed target unroutable", func() bool {
				h.action("/catalog/refresh", nil)
				r := h.mgmt("GET", "/catalog", nil)
				wantStatus(r, 200)
				var c struct {
					Models []struct {
						ID       string `json:"id"`
						Routable bool   `json:"routable"`
					} `json:"models"`
				}
				must(json.Unmarshal(r.body, &c))
				for _, m := range c.Models {
					if m.ID == "gpt-6.10-sol" && m.Routable {
						return false
					}
				}
				return true
			})
			a := h.dockAlias(h.state())
			h.action("/aliases/check", map[string]any{"alias_id": a.ID})
			p := h.state()
			require(h.dockAlias(p).Target == "gpt-6.10-sol", "removed target downgraded/cleared: %+v", h.dockAlias(p))
			before := len(h.upstream.snapshot())
			r := h.infer("/v1/chat/completions", "gpt-sol", false)
			wantStatus(r, 503)
			require(bytes.Contains(r.body, []byte("alias_target_unavailable")), "wrong unavailable code: %s", short(r.body))
			require(len(h.upstream.snapshot()) == before, "unavailable alias called upstream")
			// Restore routability for revision and restart checks without changing the saved mapping.
			h.upstream.setModels(initialModels)
			h.writeConfig()
			h.waitConfigPresent("gpt-6.10-sol")
			h.action("/catalog/refresh", nil)
			h.eventually("restored alias routability", func() bool {
				h.action("/catalog/refresh", nil)
				r := h.mgmt("GET", "/catalog", nil)
				wantStatus(r, 200)
				var c struct {
					Models []struct {
						ID       string `json:"id"`
						Routable bool   `json:"routable"`
					} `json:"models"`
				}
				must(json.Unmarshal(r.body, &c))
				for _, m := range c.Models {
					if m.ID == "gpt-6.10-sol" && m.Routable {
						return true
					}
				}
				return false
			})
			return "removed from fake + both CPA credentials; mapping kept; alias 503 with zero upstream calls; restored"
		})
	} else {
		h.rows = append(h.rows, row{"SKIP", pluginScenarios[9], "auto-update prerequisite failed"})
	}
	h.check(pluginScenarios[10], func() string {
		old := h.state()
		h.save(old)
		r := h.mgmt("PUT", "/state", old)
		wantStatus(r, 409)
		require(bytes.Contains(r.body, []byte("revision_conflict")), "wrong conflict code: %s", short(r.body))
		h.whitelistPolicy()
		return "stale PUT 409 revision_conflict; previous policy enforced"
	})
	h.check(pluginScenarios[11], func() string {
		before := h.state()
		draft := h.state()
		draft.Aliases[0].Mode = "invalid-e2e-mode"
		draft.Settings.Whitelist = false
		r := h.mgmt("PUT", "/state", draft)
		wantStatus(r, 422)
		require(bytes.Contains(r.body, []byte("validation_failed")), "wrong validation code: %s", short(r.body))
		require(reflect.DeepEqual(before, h.state()), "invalid PUT changed saved state")
		h.whitelistPolicy()
		return "invalid mode + whitelist disable rejected atomically; revision/state unchanged"
	})
	h.check(pluginScenarios[12], func() string {
		before := h.state()
		r := h.mgmt("GET", "/history?limit=100", nil)
		wantStatus(r, 200)
		var history struct {
			Entries []map[string]any `json:"entries"`
		}
		must(json.Unmarshal(r.body, &history))
		require(len(history.Entries) > 0, "no history before restart")
		ids := []string{}
		for _, e := range history.Entries {
			if id, ok := e["id"].(string); ok {
				ids = append(ids, id)
			}
		}
		require(len(ids) > 0, "history entries have no IDs")
		h.stop()
		h.start()
		h.action("/catalog/refresh", nil)
		after := h.state()
		require(reflect.DeepEqual(before, after), "state changed across restart: before=%+v after=%+v", before, after)
		r = h.mgmt("GET", "/history?limit=100", nil)
		wantStatus(r, 200)
		must(json.Unmarshal(r.body, &history))
		got := map[string]bool{}
		for _, e := range history.Entries {
			if id, ok := e["id"].(string); ok {
				got[id] = true
			}
		}
		for _, id := range ids {
			require(got[id], "history entry %s lost across restart", id)
		}
		h.whitelistPolicy()
		a := h.dockAlias(after)
		h.chat("gpt-sol", a.Target, false, "gpt-sol")
		return fmt.Sprintf("revision %d and %d history IDs restored; whitelist and alias enforce", after.Revision, len(ids))
	})
}
