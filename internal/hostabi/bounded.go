package hostabi

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
)

// CallGate bounds the caller's wait even when a synchronous native transport
// ignores context. A timed-out call keeps the slot until the transport returns;
// retries never accumulate abandoned goroutines. Share a gate across lifecycles.
type CallGate struct {
	mu   sync.Mutex
	busy bool
	idle chan struct{}
}

var ErrCallBusy = errors.New("host callback still pending")

// Call rejects overlap immediately, keeping paid retries from accumulating.
func (g *CallGate) Call(ctx context.Context, caller Caller, method string, in, out any) error {
	return g.call(ctx, caller, method, in, out, false)
}

// CallWait waits for the existing slot with the caller's deadline. Inventory
// callers can follow a cancelled lifecycle's briefly finishing callback without
// manufacturing a refresh failure; no worker is created until the slot is free.
func (g *CallGate) CallWait(ctx context.Context, caller Caller, method string, in, out any) error {
	return g.call(ctx, caller, method, in, out, true)
}

func (g *CallGate) call(ctx context.Context, caller Caller, method string, in, out any, wait bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		g.mu.Lock()
		if !g.busy {
			g.busy = true
			g.idle = make(chan struct{})
			g.mu.Unlock()
			break
		}
		idle := g.idle
		g.mu.Unlock()
		if !wait {
			return ErrCallBusy
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-idle:
		}
	}

	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	go func() {
		value := result{}
		defer func() {
			if recover() != nil {
				value.err = errors.New("host callback failed")
			}
			g.mu.Lock()
			g.busy = false
			close(g.idle)
			g.mu.Unlock()
			done <- value
		}()
		if caller == nil {
			value.err = errors.New("host callback unavailable")
			return
		}
		// Decode into worker-owned storage. A callback completing after cancellation
		// must never write into an output the caller is already using/reusing.
		value.err = caller.Call(ctx, method, in, &value.raw)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case value := <-done:
		if err := ctx.Err(); err != nil {
			return err
		}
		if value.err != nil {
			return value.err
		}
		if out != nil {
			return json.Unmarshal(value.raw, out)
		}
		return nil
	}
}
