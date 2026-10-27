// Package hostabi wraps the native callback transport behind a testable interface.
package hostabi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

type Caller interface {
	Call(context.Context, string, any, any) error
}
type Transport func(string, []byte) ([]byte, error)
type Client struct{ Transport Transport }

func (c Client) Call(ctx context.Context, method string, in, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.Transport == nil {
		return errors.New("host callback unavailable")
	}
	b, e := json.Marshal(in)
	if e != nil {
		return e
	}
	b, e = c.Transport(method, b)
	if e != nil {
		return errors.New("host callback failed")
	}
	var env pluginabi.Envelope
	if e = json.Unmarshal(b, &env); e != nil {
		return errors.New("invalid host callback envelope")
	}
	if !env.OK {
		return errors.New("host callback rejected")
	}
	if out != nil {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}
func Log(c Caller, level, message string) {
	if c != nil {
		_ = c.Call(context.Background(), pluginabi.MethodHostLog, map[string]any{"level": level, "message": message}, nil)
	}
}
