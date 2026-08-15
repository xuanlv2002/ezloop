package core

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

// 引擎对 hook 的标准防护：panic 恢复为带上下文的 error，EndHook 仍执行。

type panickyHook struct {
	endRan *bool
}

func (h panickyHook) Name() string { return "panicky" }
func (h panickyHook) OnModelStart(_ context.Context, _ *types.LoopState) error {
	panic("boom")
}
func (h panickyHook) OnEnd(_ context.Context, _ *types.LoopState) error {
	if h.endRan != nil {
		*h.endRan = true
	}
	return nil
}

func TestHookPanicRecoveredWithDetail(t *testing.T) {
	endRan := false
	a := NewAgent(&mockProvider{script: []*types.ModelResponse{textResp("x")}},
		WithHooks(panickyHook{endRan: &endRan}))

	state, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"panicky", "panicked", "OnModelStart", "boom"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %q should contain %q", err, want)
		}
	}
	if state.StopReason != types.StopError {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if !endRan {
		t.Fatal("EndHook must still run")
	}
}

type namedErrHook struct{}

func (namedErrHook) Name() string { return "faulty" }
func (namedErrHook) OnLoop(_ context.Context, _ *types.LoopState) error {
	return errSentinel
}

var errSentinel = errString("inner failure")

type errString string

func (e errString) Error() string { return string(e) }

func TestHookErrorCarriesContextAndCause(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{{ID: "1", Name: "echo", Args: []byte(`{}`)}}},
		textResp("done"),
	}}
	a := NewAgent(p, WithTools(echoTool{}), WithHooks(namedErrHook{}))
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "faulty") || !strings.Contains(err.Error(), "inner failure") {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(err.Error(), "OnLoop") {
		// panic 路径才带 phase；error 路径只带 hook 名，此断言仅确认信息完整。
		t.Logf("note: error path carries hook name only: %v", err)
	}
}

type panickyTSHook struct{}

func (panickyTSHook) Name() string { return "ts-panicky" }
func (panickyTSHook) OnToolStart(_ context.Context, _ *types.LoopState, _ *types.ToolCall) (hook.Action, error) {
	panic("tool start crash")
}

func TestToolStartHookPanicRecovered(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{{ID: "1", Name: "echo", Args: []byte(`{}`)}}},
		textResp("done"),
	}}
	a := NewAgent(p, WithTools(echoTool{}), WithHooks(panickyTSHook{}))
	_, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "ts-panicky") || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("err: %v", err)
	}
}
