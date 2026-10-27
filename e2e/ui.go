package main

import (
	"bytes"
	"fmt"
	"strings"
)

func (h *harness) uiChecks() {
	h.check("UI resources and management API", func() string {
		for _, asset := range []struct{ name, mime string }{{"index.html", "text/html"}, {"app.js", "javascript"}, {"app.css", "text/css"}, {"i18n.js", "javascript"}, {"favicon.svg", "image/svg+xml"}} {
			r := h.http("GET", "/v0/resource/plugins/cpa-model-dock/"+asset.name, nil, "")
			wantStatus(r, 200)
			require(strings.Contains(r.header.Get("Content-Type"), asset.mime), "%s content type %s", asset.name, r.header.Get("Content-Type"))
			csp := r.header.Get("Content-Security-Policy")
			require(strings.Contains(csp, "script-src 'self'") && strings.Contains(csp, "style-src 'self'") && !strings.Contains(csp, "unsafe-inline"), "%s missing strict CSP: %s", asset.name, csp)
			require(len(r.body) > 0, "empty %s", asset.name)
			if asset.name == "index.html" {
				require(!bytes.Contains(r.body, []byte("<style")) && !bytes.Contains(r.body, []byte("style=")) && bytes.Contains(r.body, []byte(`src="./app.js"`)), "inline asset or missing module")
			}
		}
		for _, path := range []string{"/overview", "/state", "/catalog", "/history?limit=2"} {
			wantStatus(h.mgmt("GET", path, nil), 200)
		}
		p := h.state()
		wantStatus(h.mgmt("POST", "/validate", p), 200)
		wantStatus(h.mgmt("POST", "/rules/preview", map[string]any{"rules": p.Rules, "settings": p.Settings, "aliases": p.Aliases}), 200)
		wantStatus(h.mgmt("POST", "/explain", map[string]any{"model": "gpt-sol"}), 200)
		wantStatus(h.mgmt("POST", "/catalog/refresh", map[string]any{}), 200)
		wantStatus(h.mgmt("POST", "/aliases/check", map[string]any{"dry_run": true}), 200)
		require(h.state().Revision == p.Revision, "read-only UI calls changed revision")
		wantStatus(h.mgmt("POST", "/aliases/check", map[string]any{"revision": p.Revision}), 200)
		require(h.state().Revision == p.Revision, "unchanged alias check bumped revision")
		return fmt.Sprintf("5 public assets with strict CSP; authenticated overview/state/catalog/history/validate/preview/explain/refresh/check; revision %d unchanged", p.Revision)
	})
}
