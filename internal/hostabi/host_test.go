package hostabi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCallbackEnvelopeAndSanitization(t *testing.T) {
	c := Client{Transport: func(method string, raw []byte) ([]byte, error) {
		if method != "host.auth.models" {
			t.Fatal(method)
		}
		var in map[string]string
		if json.Unmarshal(raw, &in) != nil || in["auth_index"] != "idx" {
			t.Fatal(string(raw))
		}
		return []byte(`{"ok":true,"result":{"credentials":[]}}`), nil
	}}
	var out struct {
		Credentials []any `json:"credentials"`
	}
	if e := c.Call(context.Background(), "host.auth.models", map[string]string{"auth_index": "idx"}, &out); e != nil || out.Credentials == nil {
		t.Fatal(e, out)
	}
	for _, failure := range []Transport{
		func(string, []byte) ([]byte, error) { return nil, errors.New("secret-token") },
		func(string, []byte) ([]byte, error) {
			return []byte(`{"ok":false,"error":{"message":"secret-token"}}`), nil
		},
	} {
		e := (Client{Transport: failure}).Call(context.Background(), "host.auth.list", struct{}{}, nil)
		if e == nil || strings.Contains(e.Error(), "secret-token") {
			t.Fatal(e)
		}
	}
}
func TestCanceledCallbackDoesNotCallHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	c := Client{Transport: func(string, []byte) ([]byte, error) { called = true; return nil, nil }}
	if c.Call(ctx, "host.auth.list", struct{}{}, nil) == nil || called {
		t.Fatal("called canceled transport")
	}
}
