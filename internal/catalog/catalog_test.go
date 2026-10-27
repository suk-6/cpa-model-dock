package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cpa-model-dock/cpa-model-dock/internal/policy"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

type fakeHost struct {
	mu          sync.Mutex
	models      string
	list        string
	runtime     map[string]string
	fail        string
	panicMethod string
	calls       map[string]int
}

func (f *fakeHost) Call(ctx context.Context, method string, in, out any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[method]++
	if method == f.panicMethod {
		panic("secret-token-host-panic")
	}
	if method == f.fail {
		return errors.New("secret-token-host-error")
	}
	var data string
	switch method {
	case pluginabi.MethodHostAuthModels:
		data = f.models
	case pluginabi.MethodHostAuthList:
		data = f.list
	case pluginabi.MethodHostAuthGetRuntime:
		wire, _ := json.Marshal(in)
		var request struct {
			AuthIndex string `json:"auth_index"`
		}
		_ = json.Unmarshal(wire, &request)
		data = f.runtime[request.AuthIndex]
	default:
		return errors.New("unexpected callback")
	}
	return json.Unmarshal([]byte(data), out)
}

func hostFixture() *fakeHost {
	return &fakeHost{
		models: `{"credentials":[{"id":"one","auth_index":"idx1","provider":"codex","models":["gpt-6-sol","gpt-6-sol","tenant/gpt-6-sol"]},{"id":"two","auth_index":"idx2","provider":"openai","models":["gpt-6-sol"]},{"id":"disabled","auth_index":"idx3","provider":"codex","models":["gpt-7-sol"]},{"id":"omitted","auth_index":"idx4","provider":"gemini","models":["gemini-3-pro"]}]}`,
		list:   `{"files":[{"id":"one","auth_index":"idx1"},{"id":"two","auth_index":"idx2"},{"id":"disabled","auth_index":"idx3","disabled":true}]}`,
		runtime: map[string]string{
			"idx1": `{"auth":{"id":"one","auth_index":"idx1","status":"active","email":"secret-email","path":"/secret-path","base_url":"https://secret-url","status_message":"secret-token"}}`,
			"idx2": `{"auth":{"id":"two","auth_index":"idx2","status":"active"}}`,
			"idx3": `{"auth":{"id":"disabled","auth_index":"idx3","status":"disabled","disabled":true}}`,
		},
	}
}

func find(t *testing.T, catalog policy.Catalog, id string) policy.Model {
	t.Helper()
	for _, model := range catalog.Models {
		if model.ID == id {
			return model
		}
	}
	t.Fatalf("missing model %s", id)
	return policy.Model{}
}

func TestSanitizedCatalogRoutabilityAndCopies(t *testing.T) {
	host := hostFixture()
	s := New(host)
	if got := s.Snapshot(); got.OK || len(got.Models) != 0 {
		t.Fatal("initial snapshot")
	}
	got := s.Refresh(context.Background())
	if !got.OK || got.RefreshedAt.IsZero() || got.Error != "" || len(got.Models) != 4 {
		t.Fatalf("refresh: %+v", got)
	}
	model := find(t, got, "gpt-6-sol")
	if !model.Routable || model.CredentialCount != 2 || strings.Join(model.Providers, ",") != "codex,openai" {
		t.Fatalf("aggregation: %+v", model)
	}
	if find(t, got, "gpt-7-sol").Routable || find(t, got, "gemini-3-pro").Routable {
		t.Fatal("disabled or unconfirmed credential marked routable")
	}
	if len(got.Prefixes) != 0 {
		t.Fatal("inferred a trusted prefix from a slash model ID")
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret") {
		t.Fatal("retained host secrets")
	}
	got.Models[0].ID = "changed"
	got.Models[0].Providers[0] = "changed"
	got.Prefixes = append(got.Prefixes, "changed")
	fresh := s.Snapshot()
	if fresh.Models[0].ID == "changed" || fresh.Models[0].Providers[0] == "changed" || len(fresh.Prefixes) != 0 {
		t.Fatal("snapshot aliases caller memory")
	}
	// Runtime-only explicit evidence from a newer host may establish a prefix.
	host.mu.Lock()
	host.runtime["idx1"] = `{"auth":{"id":"one","auth_index":"idx1","prefix":"tenant"}}`
	host.mu.Unlock()
	got = s.Refresh(context.Background())
	if !got.OK || strings.Join(got.Prefixes, ",") != "tenant" {
		t.Fatalf("explicit runtime prefix: %+v", got)
	}
	got.Prefixes[0] = "mutated"
	if s.Snapshot().Prefixes[0] != "tenant" {
		t.Fatal("prefix snapshot mutable")
	}
}

func TestCallbackFailuresKeepLastGoodAndSanitize(t *testing.T) {
	for _, method := range []string{pluginabi.MethodHostAuthModels, pluginabi.MethodHostAuthList, pluginabi.MethodHostAuthGetRuntime} {
		t.Run(method, func(t *testing.T) {
			host := hostFixture()
			s := New(host)
			good := s.Refresh(context.Background())
			host.mu.Lock()
			host.fail = method
			host.mu.Unlock()
			failed := s.Refresh(context.Background())
			if failed.OK || failed.Error == "" || strings.Contains(failed.Error, "secret") || len(failed.Models) != len(good.Models) || !find(t, failed, "gpt-6-sol").Routable || failed.RefreshedAt.Before(good.RefreshedAt) {
				t.Fatalf("failed refresh destroyed last good: %+v", failed)
			}
			host.mu.Lock()
			host.fail = ""
			host.panicMethod = method
			host.mu.Unlock()
			if got := s.Refresh(context.Background()); got.OK || strings.Contains(got.Error, "secret") || len(got.Models) != len(good.Models) {
				t.Fatal("panic failed open or leaked data")
			}
			host.mu.Lock()
			host.panicMethod = ""
			host.mu.Unlock()
			if got := s.Refresh(context.Background()); !got.OK || got.Error != "" {
				t.Fatal("did not recover from callback failure")
			}
		})
	}
}

func TestMissingRuntimeCannotBecomeEnabled(t *testing.T) {
	host := hostFixture()
	host.runtime["idx1"] = `{"auth":null}`
	got := New(host).Refresh(context.Background())
	if got.OK || len(got.Models) != 0 {
		t.Fatalf("missing runtime accepted: %+v", got)
	}
	if got := New(nil).Refresh(context.Background()); got.OK {
		t.Fatal("nil caller accepted")
	}
}

func TestConcurrentRefreshAndSnapshot(t *testing.T) {
	s := New(hostFixture())
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 8 {
				snapshot := s.Refresh(context.Background())
				if !snapshot.OK {
					t.Error(snapshot.Error)
				}
				snapshot = s.Snapshot()
				if len(snapshot.Models) > 0 {
					snapshot.Models[0].ID = "mutated"
				}
			}
		}()
	}
	wg.Wait()
	if got := s.Snapshot(); !got.OK || len(got.Models) != 4 {
		t.Fatal(got)
	}
}

func TestPollerCancellationRestartAndLiveInterval(t *testing.T) {
	host := hostFixture()
	s := New(host)
	defer s.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	notified := make(chan policy.Catalog, 4)
	s.Start(ctx, time.Millisecond, func(catalog policy.Catalog) {
		// SetInterval must be safe from inside the callback as well as while
		// an application mutex is held by management.
		s.SetInterval(time.Hour)
		notified <- catalog
	})
	select {
	case got := <-notified:
		if !got.OK {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no immediate startup refresh")
	}
	if got := time.Duration(s.interval.Load()); got != time.Hour {
		t.Fatalf("live interval %v", got)
	}
	s.SetInterval(time.Nanosecond)
	if got := time.Duration(s.interval.Load()); got != MinInterval {
		t.Fatalf("minimum not enforced: %v", got)
	}
	s.SetInterval(0)
	if got := time.Duration(s.interval.Load()); got != DefaultInterval {
		t.Fatalf("default not restored: %v", got)
	}
	cancel()
	s.Stop()
	s.Stop()
	host.mu.Lock()
	calls := host.calls[pluginabi.MethodHostAuthModels]
	host.mu.Unlock()
	if calls != 1 {
		t.Fatalf("polled below minimum: %d calls", calls)
	}
	s.Start(context.Background(), time.Hour, func(c policy.Catalog) { notified <- c })
	select {
	case <-notified:
	case <-time.After(3 * time.Second):
		t.Fatal("restart did not refresh")
	}
	s.Stop()
	canceled, stop := context.WithCancel(context.Background())
	stop()
	s.Start(canceled, time.Hour, nil)
	s.Stop()
	host.mu.Lock()
	calls = host.calls[pluginabi.MethodHostAuthModels]
	host.mu.Unlock()
	if calls != 2 {
		t.Fatalf("cancelled start called host: %d", calls)
	}
}

func TestProviderPinDoesNotBorrowOtherProviderRoutability(t *testing.T) {
	host := hostFixture()
	host.models = `{"credentials":[{"id":"one","auth_index":"idx1","provider":"codex","models":["shared"]},{"id":"disabled","auth_index":"idx3","provider":"claude","models":["shared"]}]}`
	snapshot := New(host).Refresh(context.Background())
	model := find(t, snapshot, "shared")
	if !model.Routable || strings.Join(model.Providers, ",") != "codex" {
		t.Fatalf("disabled provider inherited routability: %+v", model)
	}
	state := policy.DefaultState()
	state.Aliases = []policy.Alias{{ID: "al", Name: "friendly", Enabled: true, Mode: "pinned", Target: "shared", Provider: "claude"}}
	if d := policy.New(state, snapshot, true).Decide("friendly"); d.Allowed || d.Code != "alias_target_unavailable" {
		t.Fatal(d)
	}
}
