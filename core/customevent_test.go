package core

import (
	"context"
	"testing"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
)

// progressHook 演示扩展通过 state.EmitEvent 发送自定义事件。
type progressHook struct{}

func (progressHook) Name() string { return "progress" }

func (progressHook) OnModelEnd(_ context.Context, state *types.LoopState) error {
	state.EmitEvent("progress.model_done", map[string]any{
		"iteration": state.Iteration,
		"tokens":    state.Usage.CompletionTokens,
	})
	return nil
}

func TestHookEmitsCustomEvent(t *testing.T) {
	var custom []event.Event
	a := NewAgent(
		&mockProvider{script: []*types.ModelResponse{textResp("done")}},
		WithHooks(progressHook{}),
		WithOnEvent(func(e event.Event) {
			if e.Type == "progress.model_done" {
				custom = append(custom, e)
			}
		}),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(custom) != 1 {
		t.Fatalf("want 1 custom event, got %d", len(custom))
	}
	e := custom[0]
	if e.Iteration != 1 || e.Timestamp.IsZero() {
		t.Fatalf("event meta not filled: %+v", e)
	}
	if m, ok := e.Data.(map[string]any); !ok || m["iteration"] != 1 {
		t.Fatalf("event data: %+v", e.Data)
	}
}

// Emitter 未注入（手工构造 state）时 EmitEvent 是空操作。
func TestEmitEventNilSafe(t *testing.T) {
	state := &types.LoopState{}
	state.EmitEvent("x.y", nil) // 不应 panic
}
