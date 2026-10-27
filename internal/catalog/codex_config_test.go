package catalog

import (
	"context"
	"encoding/json"
	"testing"
)

func TestConfigCodexRegistrationsWithoutFileRuntime(t *testing.T) {
	for _, id := range []string{"codex:apikey:0123456789ab", "codex:apikey:0123456789ab-1"} {
		t.Run(id, func(t *testing.T) {
			host := &fakeHost{
				models: `{"credentials":[{"id":"` + id + `","auth_index":"cfg","provider":"codex","models":["gpt-6-sol"]}]}`,
				list:   `{"files":[]}`,
			}
			s := New(host)
			if m := find(t, s.Refresh(context.Background()), "gpt-6-sol"); !m.Routable || m.CredentialCount != 1 || len(m.Providers) != 1 || m.Providers[0] != "codex" {
				t.Fatalf("live Codex config registration lost: %+v", m)
			}
			for _, flag := range []string{`"disabled":true`, `"unavailable":true`, `"status":"disabled"`} {
				host.list = `{"files":[{"id":"` + id + `","auth_index":"cfg"}]}`
				host.runtime = map[string]string{"cfg": `{"auth":{"id":"` + id + `","auth_index":"cfg",` + flag + `}}`}
				if m := find(t, s.Refresh(context.Background()), "gpt-6-sol"); m.Routable || m.CredentialCount != 0 {
					t.Fatalf("explicit disabled/unavailable runtime overridden: %+v", m)
				}
			}
		})
	}
}

func TestCodexConfigExceptionRejectsUnconfirmedKindsAndSpoofedIDs(t *testing.T) {
	for _, c := range []credentialModels{
		{ID: "codex:apikey:0123456789ab", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789ab", AuthIndex: "cfg", Provider: "codex"},
		{ID: "codex:apikey:0123456789ab", AuthIndex: "cfg", Provider: "openai", Models: []string{"m"}},
		{ID: "xai:apikey:0123456789ab", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:oauth:0123456789ab", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789ab.json", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789AB", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789a", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789abc", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789ab-0", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789ab-01", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789ab-+1", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "codex:apikey:0123456789ab-1-2", AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
	} {
		if configRegistration(c) {
			t.Fatalf("unconfirmed Codex registration accepted: %+v", c)
		}
		if len(c.Models) > 0 {
			data, _ := json.Marshal(modelsResponse{Credentials: []credentialModels{c}})
			host := &fakeHost{models: string(data), list: `{"files":[]}`}
			if m := find(t, New(host).Refresh(context.Background()), "m"); m.Routable {
				t.Fatalf("unconfirmed Codex registration routable: %+v", c)
			}
		}
	}
}
