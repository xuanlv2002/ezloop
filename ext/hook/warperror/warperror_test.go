package warperror

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/types"
)

type panicHook struct{ endRan *bool }

func (h panicHook) Name() string { return "panicky" }
func (h panicHook) OnModelStart(_ context.Context, _ *types.LoopState) error {
	panic("boom")
}
func (h panicHook) OnEnd(_ context.Context, _ *types.LoopState) error {
	if h.endRan != nil {
		*h.endRan = true
	}
	return nil
}

type errHook struct{}

func (errHook) Name() string { return "faulty" }
func (errHook) OnModelStart(_ context.Context, _ *types.LoopState) error {
	return assertErr("inner failure")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

type okProvider struct{}

func (okProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	return &types.ModelResponse{Content: "ok"}, nil
}

func TestWrapRecoversPanicAsError(t *testing.T) {
	endRan := false
	a := core.NewAgent(okProvider{}, core.WithHooks(Wrap(panicHook{endRan: &endRan})))
	state, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error from wrapped panic")
	}
	if !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopError {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if !endRan {
		t.Fatal("inner EndHook should still run")
	}
}

func TestWrapAddsContextToError(t *testing.T) {
	a := core.NewAgent(okProvider{}, core.WithHooks(Wrap(errHook{})))
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "faulty") || !strings.Contains(err.Error(), "inner failure") {
		t.Fatalf("err should contain hook name and cause: %v", err)
	}
}

type panicTool struct{}

func (panicTool) Name() string            { return "boom" }
func (panicTool) Description() string     { return "" }
func (panicTool) ArgsSchema() json.RawMessage { return nil }
func (panicTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	panic("tool crash")
}

func TestWrapToolRecoversPanic(t *testing.T) {
	out, err := WrapTool(panicTool{}).Invoke(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err: %v", err)
	}
	if out != "" {
		t.Fatalf("out: %q", out)
	}
}
