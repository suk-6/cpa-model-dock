package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/suk-6/cpa-model-dock/internal/policy"
)

func emptyHost() *fakeHost {
	return &fakeHost{models: `{"credentials":[]}`, list: `{"files":[]}`}
}

func enableFixture(host *fakeHost) {
	fixture := hostFixture()
	host.mu.Lock()
	defer host.mu.Unlock()
	host.models, host.list, host.runtime = fixture.models, fixture.list, fixture.runtime
	host.fail = ""
}

func receiveCatalog(t *testing.T, updates <-chan policy.Catalog) policy.Catalog {
	t.Helper()
	select {
	case c := <-updates:
		return c
	default:
		t.Fatal("missing catalog notification")
		return policy.Catalog{}
	}
}

func noCatalog(t *testing.T, updates <-chan policy.Catalog) {
	t.Helper()
	select {
	case c := <-updates:
		t.Fatalf("unexpected catalog notification: %+v", c)
	default:
	}
}

func TestBootstrapEmptyRetriesBecomeReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := emptyHost()
		s := New(host)
		defer s.Stop()
		updates := make(chan policy.Catalog, 10)
		s.Start(context.Background(), time.Hour, func(c policy.Catalog) { updates <- c })
		synctest.Wait()
		first := receiveCatalog(t, updates)
		if first.OK || first.Stale || len(first.Models) != 0 || first.Warning != "no_models" || !first.LastGoodAt.IsZero() || first.Generation != 1 {
			t.Fatalf("empty startup became authoritative: %+v", first)
		}
		time.Sleep(2*time.Second - time.Nanosecond)
		synctest.Wait()
		noCatalog(t, updates)
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		second := receiveCatalog(t, updates)
		if second.OK || second.Generation != 2 {
			t.Fatal(second)
		}
		enableFixture(host)
		time.Sleep(4 * time.Second)
		synctest.Wait()
		ready := receiveCatalog(t, updates)
		if !ready.OK || ready.Stale || ready.Warning != "" || ready.Error != "" || ready.LastGoodAt.IsZero() || ready.Generation != 3 || len(ready.Models) != 4 {
			t.Fatalf("did not become ready on retry: %+v", ready)
		}
		time.Sleep(30 * time.Second)
		synctest.Wait()
		noCatalog(t, updates)
	})
}

func TestBootstrapBackoffCapsAndEndsAtNormalInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := New(emptyHost())
		defer s.Stop()
		updates := make(chan policy.Catalog, 10)
		s.Start(context.Background(), MinInterval, func(c policy.Catalog) { updates <- c })
		synctest.Wait()
		receiveCatalog(t, updates)
		// Retry at t=2,6,14,22; t=30 is a normal poll. No short retries
		// remain after that first normal interval, even if still empty.
		for _, delay := range []time.Duration{2, 4, 8, 8, 8} {
			time.Sleep(delay * time.Second)
			synctest.Wait()
			receiveCatalog(t, updates)
			noCatalog(t, updates)
		}
		time.Sleep(29 * time.Second)
		synctest.Wait()
		noCatalog(t, updates)
		time.Sleep(time.Second)
		synctest.Wait()
		if c := receiveCatalog(t, updates); c.Generation != 7 {
			t.Fatal(c)
		}
	})
}

func TestBootstrapFailureManualRecoveryAndRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		host := hostFixture()
		host.fail = pluginabi.MethodHostAuthModels
		s := New(host)
		defer s.Stop()
		updates := make(chan policy.Catalog, 10)
		ctx, cancel := context.WithCancel(context.Background())
		s.Start(ctx, time.Hour, func(c policy.Catalog) { updates <- c })
		synctest.Wait()
		if c := receiveCatalog(t, updates); c.OK || c.Error == "" {
			t.Fatal(c)
		}
		cancel()
		s.Stop()
		time.Sleep(10 * time.Second)
		synctest.Wait()
		noCatalog(t, updates)
		// A replacement poller gets a fresh immediate attempt and retry window.
		s.Start(context.Background(), time.Hour, func(c policy.Catalog) { updates <- c })
		synctest.Wait()
		receiveCatalog(t, updates)
		host.mu.Lock()
		host.fail = ""
		host.mu.Unlock()
		if c := s.Refresh(context.Background()); !c.OK {
			t.Fatal(c)
		}
		time.Sleep(20 * time.Second)
		synctest.Wait()
		noCatalog(t, updates) // manual fresh result also ends bootstrap retries
	})
}

type recoveryHostFunc func(context.Context, string, any, any) error

func (f recoveryHostFunc) Call(ctx context.Context, method string, in, out any) error {
	return f(ctx, method, in, out)
}

func TestReplacingPollerCancelsOldAttemptBeforeNewPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := hostFixture()
		entered, release := make(chan struct{}), make(chan struct{})
		first := true
		host := recoveryHostFunc(func(ctx context.Context, method string, in, out any) error {
			if method == pluginabi.MethodHostAuthModels && first {
				first = false
				close(entered)
				<-release // simulate an uncancellable native callback
			}
			return base.Call(ctx, method, in, out)
		})
		s := New(host)
		defer s.Stop()
		updates := make(chan policy.Catalog, 10)
		s.Start(context.Background(), time.Hour, func(c policy.Catalog) { updates <- c })
		<-entered
		s.Start(context.Background(), time.Hour, func(c policy.Catalog) { updates <- c })
		synctest.Wait()
		if c := s.Snapshot(); c.OK || c.Generation != 1 || c.Error == "" {
			t.Fatal("cancelled accepted attempt did not publish failure", c)
		}
		noCatalog(t, updates) // callbacks from the cancelled lifecycle are suppressed
		close(release)
		synctest.Wait()
		fresh := receiveCatalog(t, updates)
		if !fresh.OK || fresh.Generation != 2 || fresh.Stale {
			t.Fatal("replacement poller failed to recover", fresh)
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		noCatalog(t, updates)
		if c := s.Snapshot(); c.Generation != fresh.Generation || !c.OK {
			t.Fatal("old native completion replaced new snapshot", c)
		}
	})
}

func TestPersistedFactsDoNotSuppressEmptyStartupRetries(t *testing.T) {
	dir := t.TempDir()
	good := NewPersistent(hostFixture(), dir).Refresh(context.Background())
	synctest.Test(t, func(t *testing.T) {
		host := emptyHost()
		s := NewPersistent(host, dir)
		defer s.Stop()
		updates := make(chan policy.Catalog, 10)
		s.Start(context.Background(), time.Hour, func(c policy.Catalog) { updates <- c })
		synctest.Wait()
		if c := receiveCatalog(t, updates); c.OK || !c.Stale || c.Warning != "no_models_using_last_known" || !c.LastGoodAt.Equal(good.LastGoodAt) {
			t.Fatal("empty startup did not preserve persisted facts", c)
		}
		enableFixture(host)
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if c := receiveCatalog(t, updates); !c.OK || c.Stale || c.Warning != "" || c.Generation != 2 {
			t.Fatal("persisted snapshot incorrectly suppressed bootstrap retry", c)
		}
	})
}

func TestEmptyKeepsLastGoodAndFreshPartialRemovesMissing(t *testing.T) {
	host := hostFixture()
	dir := t.TempDir()
	s := NewPersistent(host, dir)
	good := s.Refresh(context.Background())
	before, err := os.ReadFile(filepath.Join(dir, cacheFilename))
	if err != nil {
		t.Fatal(err)
	}
	host.models = `{"credentials":[{"id":"one","auth_index":"idx1","models":[" ",""]}]}`
	empty := s.Refresh(context.Background())
	if empty.OK || !empty.Stale || empty.Warning != "no_models_using_last_known" || empty.Error != "" || !empty.LastGoodAt.Equal(good.LastGoodAt) || !reflect.DeepEqual(empty.Models, good.Models) || empty.Generation != good.Generation+1 {
		t.Fatalf("empty refresh erased facts: %+v", empty)
	}
	if s.WithCurrentGeneration(good.Generation, func() { t.Error("stale generation committed") }) {
		t.Fatal("empty refresh did not fence prior generation")
	}
	after, _ := os.ReadFile(filepath.Join(dir, cacheFilename))
	if !bytes.Equal(before, after) {
		t.Fatal("empty response replaced persisted last-good snapshot")
	}
	host.fail = pluginabi.MethodHostAuthModels
	failed := s.Refresh(context.Background())
	if failed.OK || !failed.Stale || failed.Warning != "catalog_unavailable_using_last_known" || !failed.LastGoodAt.Equal(good.LastGoodAt) {
		t.Fatal(failed)
	}
	after, _ = os.ReadFile(filepath.Join(dir, cacheFilename))
	if !bytes.Equal(before, after) {
		t.Fatal("callback failure replaced persisted last-good snapshot")
	}
	host.fail = ""
	host.models = `{"credentials":[{"id":"one","auth_index":"idx1","provider":"codex","models":["gpt-6-sol"]}]}`
	partial := s.Refresh(context.Background())
	if !partial.OK || partial.Stale || partial.Warning != "" || partial.Error != "" || len(partial.Models) != 1 || partial.Models[0].ID != "gpt-6-sol" || partial.Models[0].CredentialCount != 1 {
		t.Fatalf("fresh partial inventory did not remove absent models: %+v", partial)
	}
	if restored := NewPersistent(nil, dir).Snapshot(); len(restored.Models) != 1 || !reflect.DeepEqual(restored.Models, partial.Models) {
		t.Fatalf("partial removal not persisted: %+v", restored)
	}
}

func TestPersistentLoadSanitizesAndRemainsStaleUntilLiveRecovery(t *testing.T) {
	host := hostFixture()
	host.runtime["idx1"] = `{"auth":{"id":"one","auth_index":"idx1","prefix":"private-runtime-prefix","email":"secret-email","path":"/secret-path","base_url":"https://secret-url","status_message":"secret-token"}}`
	dir := filepath.Join(t.TempDir(), "state")
	good := NewPersistent(host, dir).Refresh(context.Background())
	data, err := os.ReadFile(filepath.Join(dir, cacheFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secret", "private-runtime-prefix", "auth_index", "prefix", "refreshed_at", "generation", `"one"`, `"idx1"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("cache retained private or unneeded field %q: %s", forbidden, data)
		}
	}
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil || len(wire) != 3 || wire["models"] == nil || wire["version"] == nil || wire["last_good_at"] == nil {
		t.Fatalf("unexpected persisted schema: %s (%v)", data, err)
	}
	info, err := os.Stat(filepath.Join(dir, cacheFilename))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("cache file permissions: %v, %v", info, err)
	}
	restarted := NewPersistent(emptyHost(), dir)
	loaded := restarted.Snapshot()
	if loaded.OK || !loaded.Stale || loaded.Warning != "using_persisted_catalog" || loaded.Generation != 0 || !loaded.LastGoodAt.Equal(good.LastGoodAt) || !reflect.DeepEqual(loaded.Models, good.Models) || len(loaded.Prefixes) != 0 {
		t.Fatalf("loaded cache incorrectly fresh or lost facts: %+v", loaded)
	}
	loaded.Models[0].Providers[0] = "mutated"
	if restarted.Snapshot().Models[0].Providers[0] == "mutated" {
		t.Fatal("loaded snapshot aliases caller memory")
	}
	empty := restarted.Refresh(context.Background())
	if empty.OK || !empty.Stale || empty.Warning != "no_models_using_last_known" || !reflect.DeepEqual(empty.Models, good.Models) {
		t.Fatal(empty)
	}
	enableFixture(restarted.caller.(*fakeHost))
	fresh := restarted.Refresh(context.Background())
	if !fresh.OK || fresh.Stale || fresh.Warning != "" || fresh.Generation != 2 {
		t.Fatal(fresh)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != cacheFilename {
		t.Fatal("left temporary cache files behind", entries)
	}
}

func TestCorruptPersistentCatalogFallsBackAndRecovers(t *testing.T) {
	const stamp = `"last_good_at":"2026-01-01T00:00:00Z"`
	for name, data := range map[string]string{
		"truncated":      `{"version":1,`,
		"wrong_version":  `{"version":2,` + stamp + `,"models":[{"id":"m"}]}`,
		"empty":          `{"version":1,` + stamp + `,"models":[]}`,
		"no_time":        `{"version":1,"models":[{"id":"m"}]}`,
		"negative_count": `{"version":1,` + stamp + `,"models":[{"id":"m","credential_count":-1}]}`,
		"bad_routable":   `{"version":1,` + stamp + `,"models":[{"id":"m","routable":true}]}`,
		"duplicate":      `{"version":1,` + stamp + `,"models":[{"id":"m"},{"id":"m"}]}`,
		"secret_field":   `{"version":1,` + stamp + `,"models":[{"id":"m","auth_index":"secret"}]}`,
		"trailing":       `{"version":1,` + stamp + `,"models":[{"id":"m"}]}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, cacheFilename), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			s := NewPersistent(hostFixture(), dir)
			if got := s.Snapshot(); got.OK || got.Stale || len(got.Models) != 0 || got.Warning != "catalog_cache_unavailable" || !got.LastGoodAt.IsZero() {
				t.Fatalf("accepted corrupt cache: %+v", got)
			}
			if got := s.Refresh(context.Background()); !got.OK || got.Stale || got.Warning != "" {
				t.Fatalf("live recovery failed: %+v", got)
			}
			if got := NewPersistent(nil, dir).Snapshot(); len(got.Models) != 4 || !got.Stale {
				t.Fatal("recovery did not replace corrupt cache", got)
			}
		})
	}
}

func TestUnavailableCacheDoesNotInvalidateLiveFacts(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private-state-path")
	if err := os.WriteFile(dir, []byte("file, not directory"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewPersistent(hostFixture(), dir)
	got := s.Refresh(context.Background())
	if !got.OK || got.Stale || got.Warning != "catalog_cache_unavailable" || got.Error != "" || len(got.Models) != 4 || got.LastGoodAt.IsZero() {
		t.Fatal("cache failure prevented live recovery", got)
	}
	data, _ := json.Marshal(got)
	if strings.Contains(string(data), "private-state-path") {
		t.Fatal("filesystem error leaked into status")
	}
	if got := NewPersistent(hostFixture(), "").Refresh(context.Background()); !got.OK || got.Warning != "" {
		t.Fatal("empty stateDir did not disable caching", got)
	}
}
