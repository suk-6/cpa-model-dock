package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/suk-6/cpa-model-dock/internal/hostabi"
	"github.com/suk-6/cpa-model-dock/internal/policy"
	"github.com/suk-6/cpa-model-dock/internal/store"
)

type blockedProbeHost struct {
	entered, release chan struct{}
	once             sync.Once
	calls            atomic.Int32
	failInventory    atomic.Bool
}

func (h *blockedProbeHost) Call(ctx context.Context, method string, in, out any) error {
	if method == pluginabi.MethodHostModelExecute {
		h.calls.Add(1)
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	if h.failInventory.Load() && method == pluginabi.MethodHostAuthModels {
		return errors.New("private host failure")
	}
	return fakeHost().Call(ctx, method, in, out)
}
func newBlockedProbeHost(t *testing.T) *blockedProbeHost {
	t.Helper()
	h := &blockedProbeHost{entered: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() {
		close(h.release)
		deadline := time.Now().Add(time.Second)
		for {
			err := healthProbes.Call(context.Background(), fakeHost(), pluginabi.MethodHostModelExecute, nil, nil)
			if !errors.Is(err, hostabi.ErrCallBusy) {
				if err != nil {
					t.Error(err)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Error("probe worker did not retire")
				return
			}
			goruntime.Gosched()
		}
	})
	return h
}
func awaitLifecycle[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("operation waited for blocked host callback")
		var zero T
		return zero
	}
}
func prepareProbeApp(t *testing.T, h hostabi.Caller) *application {
	t.Helper()
	a, err := newApplication(t.TempDir(), h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	a.refresh(context.Background())
	s := autoState()
	s.Aliases[0].Auto.HealthCheck = true
	if _, err = seedApplication(a, s); err != nil {
		t.Fatal(err)
	}
	return a
}
func beginProbe(t *testing.T, a *application, h *blockedProbeHost) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	go func() { _, err := a.checkRevision(context.Background(), 1, "al_one", false); done <- err }()
	awaitLifecycle(t, h.entered)
	return done
}

func TestPaidProbeDoesNotLockSavesAndCannotOverwriteNewPolicy(t *testing.T) {
	h := newBlockedProbeHost(t)
	a := prepareProbeApp(t, h)
	done := beginProbe(t, a, h)
	saved := make(chan error, 1)
	go func() {
		s := a.current.Load().StateCopy()
		s.Aliases[0].Mode = "pinned"
		_, err := a.save(1, s, "user", "state", "pin while checking")
		saved <- err
	}()
	if err := awaitLifecycle(t, saved); err != nil {
		t.Fatal(err)
	}
	// Complete the old probe after the newer policy was saved.
	h.release <- struct{}{}
	if err := awaitLifecycle(t, done); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatal(err)
	}
	if s := a.current.Load().StateCopy(); s.Revision != 2 || s.Aliases[0].Mode != "pinned" || s.Aliases[0].Target != "gpt-6.1-sol" {
		t.Fatal(s)
	}
	if len(a.status) != 0 || len(a.store.History(10, "")) != 2 {
		t.Fatal("stale probe published status/history")
	}
}

func TestProbeCommitRejectsNewCatalogBeforeDelivery(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failed], func(t *testing.T) {
			h := newBlockedProbeHost(t)
			a := prepareProbeApp(t, h)
			done := beginProbe(t, a, h)
			old := a.current.Load().CatalogCopy()
			h.failInventory.Store(failed)
			newer := a.catalog.Refresh(context.Background()) // deliberately delay onCatalog delivery
			if newer.Generation <= old.Generation || newer.OK == failed {
				t.Fatal(newer)
			}
			// Let the native callback finish successfully; only the generation fence can reject it.
			h.release <- struct{}{}
			if err := awaitLifecycle(t, done); !errors.Is(err, store.ErrRevisionConflict) {
				t.Fatal(err)
			}
			if a.current.Load().StateCopy().Revision != 1 || len(a.status) != 0 || len(a.store.History(10, "")) != 1 {
				t.Fatal("stale result committed")
			}
		})
	}
}

func TestOutOfOrderCatalogDeliveryNeverPublishesOrEvaluatesOldSuccess(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_success", true: "new_failure"}[failed], func(t *testing.T) {
			h := newBlockedProbeHost(t)
			a, err := newApplication(t.TempDir(), h)
			if err != nil {
				t.Fatal(err)
			}
			defer a.close()
			if _, err = seedApplication(a, autoState()); err != nil {
				t.Fatal(err)
			}
			older := a.catalog.Refresh(context.Background())
			h.failInventory.Store(failed)
			newer := a.catalog.Refresh(context.Background())
			a.onCatalog(newer)
			before := a.current.Load()
			a.onCatalog(older)
			if a.current.Load() != before || before.CatalogCopy().Generation != newer.Generation {
				t.Fatal("old completion replaced newer publication")
			}
			wantRevision := int64(2)
			if failed {
				wantRevision = 1
			}
			if before.StateCopy().Revision != wantRevision {
				t.Fatal("stale success evaluated", before.StateCopy())
			}
		})
	}
}

func TestRuntimeLifecycleCancelsManagementReaderBeforeWriteLock(t *testing.T) {
	for _, action := range []string{"shutdown", "same_directory", "new_directory"} {
		t.Run(action, func(t *testing.T) {
			h := newBlockedProbeHost(t)
			r := newRuntime(h)
			defer r.Close()
			dir := t.TempDir()
			registerTest(t, r, 7, dir)
			a := r.app
			a.catalog.Stop() // isolate this manual management reader from the poller
			a.refresh(context.Background())
			s := autoState()
			s.Aliases[0].Auto.HealthCheck = true
			if _, err := seedApplication(a, s); err != nil {
				t.Fatal(err)
			}
			checkDone := make(chan []byte, 1)
			go func() {
				checkDone <- r.Dispatch(pluginabi.MethodManagementHandle, mustJSON(pluginapi.ManagementRequest{Method: "POST", Path: "/v0/management/plugins/cpa-model-dock/aliases/check", Body: []byte(`{"revision":1,"alias_id":"al_one"}`)}))
			}()
			awaitLifecycle(t, h.entered)
			lifecycleDone := make(chan []byte, 1)
			go func() {
				if action == "shutdown" {
					r.Close()
					lifecycleDone <- nil
					return
				}
				if action == "new_directory" {
					dir = filepath.Join(t.TempDir(), "replacement")
				}
				lifecycleDone <- r.Dispatch(pluginabi.MethodPluginReconfigure, mustJSON(map[string]any{"schema_version": 7, "config_yaml": []byte("state-dir: " + dir + "\n")}))
			}()
			raw := awaitLifecycle(t, lifecycleDone)
			awaitLifecycle(t, checkDone)
			if action != "shutdown" {
				var env pluginabi.Envelope
				if json.Unmarshal(raw, &env) != nil || !env.OK {
					t.Fatal(string(raw))
				}
				// A fresh lifecycle must not launch more workers while the old native call is pending.
				if err := r.app.probe(context.Background(), s.Aliases[0], policy.Model{ID: "gpt-6.2-sol"}); err == nil {
					t.Fatal("second paid callback admitted")
				}
			}
			if h.calls.Load() != 1 || (action != "new_directory" && a.current.Load().StateCopy().Revision != 1) {
				t.Fatal("late probe mutated state or accumulated workers", h.calls.Load())
			}
		})
	}
}

func TestPollerStopDoesNotDrainUnresponsiveProbe(t *testing.T) {
	h := newBlockedProbeHost(t)
	a := prepareProbeApp(t, h)
	a.start(time.Hour)
	awaitLifecycle(t, h.entered)
	stopped := make(chan struct{})
	go func() { a.stop(); close(stopped) }()
	awaitLifecycle(t, stopped)
	if a.current.Load().StateCopy().Revision != 1 {
		t.Fatal("cancelled poller committed")
	}
}

func TestFailedDirectoryReconfigureResumesExistingLifecycle(t *testing.T) {
	r := newRuntime(fakeHost())
	defer r.Close()
	registerTest(t, r, 7, t.TempDir())
	old := r.app
	bad := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(bad, []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	var env pluginabi.Envelope
	raw := r.Dispatch(pluginabi.MethodPluginReconfigure, mustJSON(map[string]any{"schema_version": 7, "config_yaml": []byte("state-dir: " + bad + "\n")}))
	if json.Unmarshal(raw, &env) != nil || env.OK || r.app != old || old.workContext().Err() != nil {
		t.Fatal("failed replacement retired last good lifecycle", string(raw))
	}
	if _, err := old.save(0, autoState(), "user", "state", "still editable"); err != nil {
		t.Fatal(err)
	}
}
