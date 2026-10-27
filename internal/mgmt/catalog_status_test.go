package mgmt

import (
	"github.com/suk-6/cpa-model-dock/internal/policy"
	"testing"
	"time"
)

func TestCatalogReadinessViews(t *testing.T) {
	for _, warning := range []string{"using_persisted_catalog", "no_models_using_last_known", "no_models", "catalog_unavailable_using_last_known", "catalog_cache_unavailable"} {
		t.Run(warning, func(t *testing.T) {
			f := setup(t)
			s := f.current.Load()
			c := s.CatalogCopy()
			c.OK = false
			c.Stale = true
			c.Warning = warning
			c.LastGoodAt = time.Now().UTC()
			f.current.Store(policy.New(s.StateCopy(), c, true))
			got := decode(t, call(t, f.h, "GET", "/catalog", nil), 200)
			if got["ok"] != false || got["stale"] != true || got["warning"] != warning {
				t.Fatal(got)
			}
			detail := got["warning_detail"].(map[string]any)
			if detail["code"] != warning {
				t.Fatal(detail)
			}
			reasonShape(t, detail)
			overview := decode(t, call(t, f.h, "GET", "/overview", nil), 200)
			found := false
			for _, w := range overview["warnings"].([]any) {
				if w.(map[string]any)["code"] == warning {
					found = true
				}
			}
			if !found {
				t.Fatal(overview)
			}
		})
	}
}
