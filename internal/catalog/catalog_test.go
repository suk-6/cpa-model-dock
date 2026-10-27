package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/suk-6/cpa-model-dock/internal/policy"
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

func TestConfigOpenAIRegistrationsWithoutFileRuntime(t *testing.T) {
	const id = "openai-compatibility:fixture:0123456789ab"
	host := &fakeHost{models: `{"credentials":[{"id":"` + id + `","auth_index":"cfg","provider":"openai-compatible-fixture","models":["gpt-6.1-sol"]}]}`, list: `{"files":[]}`}
	s := New(host)
	if m := find(t, s.Refresh(context.Background()), "gpt-6.1-sol"); !m.Routable || m.CredentialCount != 1 {
		t.Fatalf("live config registration lost: %+v", m)
	}
	// If runtime evidence exists, its disabled/unavailable flag wins.
	host.list = `{"files":[{"id":"` + id + `","auth_index":"cfg"}]}`
	host.runtime = map[string]string{"cfg": `{"auth":{"id":"` + id + `","auth_index":"cfg","disabled":true}}`}
	if m := find(t, s.Refresh(context.Background()), "gpt-6.1-sol"); m.Routable {
		t.Fatalf("disabled config enabled: %+v", m)
	}
	host.models = `{"credentials":[]}`
	if got := s.Refresh(context.Background()); got.OK || !got.Stale || len(got.Models) != 1 || got.Warning != "no_models_using_last_known" {
		t.Fatalf("empty config inventory erased last-known registration: %+v", got)
	}
	for _, c := range []credentialModels{
		{ID: id, AuthIndex: "cfg", Provider: "codex", Models: []string{"m"}},
		{ID: "oauth:fixture:0123456789ab", AuthIndex: "cfg", Provider: "openai-compatible-fixture", Models: []string{"m"}},
		{ID: id, Provider: "openai-compatible-fixture", Models: []string{"m"}},
		{ID: id, AuthIndex: "cfg", Provider: "openai-compatible-fixture"},
	} {
		if configRegistration(c) {
			t.Fatalf("unconfirmed registration enabled: %+v", c)
		}
	}
}

func TestRefreshGenerationsIncludeFailuresAndFencePublication(t *testing.T) {
	host := hostFixture()
	s := New(host)
	if s.Snapshot().Generation != 0 {
		t.Fatal("initial generation")
	}
	good := s.Refresh(context.Background())
	host.fail = pluginabi.MethodHostAuthModels
	failed := s.Refresh(context.Background())
	if good.Generation != 1 || failed.Generation != 2 || failed.OK {
		t.Fatal(good, failed)
	}
	ran := false
	if s.WithCurrentGeneration(good.Generation, func() { ran = true }) || ran {
		t.Fatal("stale generation accepted")
	}
	if !s.WithCurrentGeneration(failed.Generation, func() { ran = true }) || !ran {
		t.Fatal("current generation rejected")
	}
	host.fail = ""
	recovered := s.Refresh(context.Background())
	if recovered.Generation != 3 || !recovered.OK {
		t.Fatal(recovered)
	}
	raw, _ := json.Marshal(recovered)
	if strings.Contains(string(raw), "generation") {
		t.Fatal("runtime generation leaked onto API", string(raw))
	}
}

type blockedInventoryHost struct {
	base             *fakeHost
	entered, release chan struct{}
}

func (h *blockedInventoryHost) Call(ctx context.Context, method string, in, out any) error {
	if method == pluginabi.MethodHostAuthModels {
		close(h.entered)
		<-h.release
	}
	return h.base.Call(ctx, method, in, out)
}

func TestCancelledQueuedRefreshDoesNotAttemptOrPublish(t *testing.T) {
	host := &blockedInventoryHost{base: hostFixture(), entered: make(chan struct{}), release: make(chan struct{})}
	s := New(host)
	first := make(chan policy.Catalog, 1)
	go func() { first <- s.Refresh(context.Background()) }()
	<-host.entered // the first refresh owns the slot and is inside the callback
	// A queue wait is not an accepted refresh attempt. Cancelling it cannot
	// manufacture failure evidence about a host callback that never ran.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	queued := s.Refresh(ctx)
	if ctx.Err() != context.DeadlineExceeded || queued.Generation != 0 || s.Snapshot().Generation != 0 {
		t.Fatal(queued, ctx.Err())
	}
	close(host.release)
	select {
	case good := <-first:
		// The older accepted operation may complete: the queued request was not a
		// newer failed host observation. It never called the host or claimed a gen.
		if !good.OK || good.Generation != 1 || host.base.calls[pluginabi.MethodHostAuthModels] != 1 {
			t.Fatal(good)
		}
	case <-time.After(time.Second):
		t.Fatal("accepted refresh failed to finish")
	}
}

func TestStartedCancelledRefreshPublishesFailure(t *testing.T) {
	host := &blockedInventoryHost{base: hostFixture(), entered: make(chan struct{}), release: make(chan struct{})}
	defer close(host.release)
	s := New(host)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan policy.Catalog, 1)
	go func() { done <- s.Refresh(ctx) }()
	<-host.entered
	cancel()
	select {
	case failed := <-done:
		if failed.OK || failed.Generation != 1 || failed.Error != "host model inventory unavailable" {
			t.Fatal(failed)
		}
		if s.Snapshot().Generation != 1 {
			t.Fatal("accepted cancellation did not publish")
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled refresh waited for synchronous callback")
	}
}

func TestConfigProviderKeyCanonicalSpecialNames(t *testing.T) {
	for _, name := range []string{"openai-compatibility", "openai-compatible-fixture"} {
		t.Run(name, func(t *testing.T) {
			id := "openai-compatibility:" + name + ":0123456789ab"
			host := &fakeHost{models: `{"credentials":[{"id":"` + id + `","auth_index":"cfg","provider":"` + name + `","models":["m"]}]}`, list: `{"files":[]}`}
			if m := find(t, New(host).Refresh(context.Background()), "m"); !m.Routable || m.CredentialCount != 1 {
				t.Fatal(m)
			}
			base := credentialModels{ID: id, AuthIndex: "cfg", Provider: name, Models: []string{"m"}}
			if !configRegistration(base) {
				t.Fatal(base)
			}
			for _, bad := range []credentialModels{
				{ID: id, Provider: name, Models: []string{"m"}},
				{ID: id, AuthIndex: "cfg", Provider: name},
				{ID: id, AuthIndex: "cfg", Provider: "openai-compatible-" + name, Models: []string{"m"}},
				{ID: "openai-compatibility:" + name + ":bad", AuthIndex: "cfg", Provider: name, Models: []string{"m"}},
			} {
				if configRegistration(bad) {
					t.Fatal("unconfirmed special registration accepted", bad)
				}
			}
		})
	}
}
