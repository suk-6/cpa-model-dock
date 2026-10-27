package hostabi

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

type callerFunc func(context.Context, string, any, any) error

func (f callerFunc) Call(ctx context.Context, m string, in, out any) error { return f(ctx, m, in, out) }

func TestCallGateCancellationOwnsOutputAndBoundsPendingWork(t *testing.T) {
	var gate CallGate
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	host := callerFunc(func(ctx context.Context, _ string, _ any, out any) error {
		calls.Add(1)
		close(entered)
		<-release // deliberately ignore ctx, like synchronous C
		return json.Unmarshal([]byte(`{"value":42}`), out)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := struct{ Value int }{Value: 7}
	done := make(chan error, 1)
	go func() { done <- gate.Call(ctx, host, "probe", nil, &output) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel waited for native callback")
	}
	for range 50 {
		if err := gate.Call(context.Background(), host, "probe", nil, &output); !errors.Is(err, ErrCallBusy) {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 || output.Value != 7 {
		t.Fatal("pending calls/output", calls.Load(), output)
	}
	close(release)
	// Wait for slot release without starting another real host call.
	deadline := time.After(time.Second)
	for {
		gate.mu.Lock()
		busy := gate.busy
		gate.mu.Unlock()
		if !busy {
			break
		}
		select {
		case <-deadline:
			t.Fatal("slot remained busy")
		default:
			runtime.Gosched()
		}
	}
	if output.Value != 7 {
		t.Fatal("late native completion mutated caller output", output)
	}
	fast := callerFunc(func(_ context.Context, _ string, _ any, out any) error {
		return json.Unmarshal([]byte(`{"value":99}`), out)
	})
	if err := gate.Call(context.Background(), fast, "probe", nil, &output); err != nil || output.Value != 99 {
		t.Fatal(err, output)
	}
}

func TestCallGateDeadlinePanicAndCancelledEntry(t *testing.T) {
	var gate CallGate
	panics := callerFunc(func(context.Context, string, any, any) error { panic("secret") })
	if err := gate.Call(context.Background(), panics, "probe", nil, nil); err == nil || err.Error() != "host callback failed" {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := gate.Call(ctx, panics, "probe", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	host := callerFunc(func(context.Context, string, any, any) error { close(entered); <-release; return nil })
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	defer close(release)
	if err := gate.Call(ctx, host, "probe", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	<-entered
}

func TestCallGateWaitCancelsAndDoesNotStartAnotherWorker(t *testing.T) {
	var gate CallGate
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	host := callerFunc(func(_ context.Context, _ string, _ any, out any) error {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return json.Unmarshal([]byte(`{}`), out)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { first <- gate.Call(ctx, host, "first", nil, nil) }()
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	waitCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	if err := gate.CallWait(waitCtx, host, "queued", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("queued waiter spawned a worker")
	}
	close(release)
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := gate.CallWait(ctx2, host, "next", nil, nil); err != nil || calls.Load() != 2 {
		t.Fatal(err, calls.Load())
	}
}
