package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

// tagToolWarp 记录包装顺序。
func tagToolWarp(tag string, log *[]string) types.ToolWarpHandler {
	return func(inner types.Tool) types.Tool {
		return &taggedTool{tag: tag, inner: inner, log: log}
	}
}

type taggedTool struct {
	tag   string
	inner types.Tool
	log   *[]string
}

func (t *taggedTool) Name() string                { return t.inner.Name() }
func (t *taggedTool) Description() string         { return t.inner.Description() }
func (t *taggedTool) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }

func (t *taggedTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	*t.log = append(*t.log, "enter:"+t.tag)
	out, err := t.inner.Invoke(ctx, args)
	*t.log = append(*t.log, "exit:"+t.tag)
	return out, err
}

// injectHook 在 OnStart 时注入工具，模拟 mcp router 等动态注册。
type injectHook struct{ tool types.Tool }

func (h injectHook) Name() string { return "inject" }
func (h injectHook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(h.tool)
	return nil
}

type upperTool struct{}

func (upperTool) Name() string                { return "upper" }
func (upperTool) Description() string         { return "" }
func (upperTool) ArgsSchema() json.RawMessage { return nil }
func (upperTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	return "UP:" + string(args), nil
}

func TestToolWarpWrapsStaticAndInjectedTools(t *testing.T) {
	var log []string
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{
			{ID: "1", Name: "upper", Args: json.RawMessage(`a`)},
			{ID: "2", Name: "injected", Args: json.RawMessage(`b`)},
		}},
		textResp("done"),
	}}

	// injected 工具本体：直接返回原文。
	injected := &upperTool{}
	// 改名以区分——通过包装实现。
	renamed := &renamedTool{inner: injected, name: "injected"}

	a := NewAgent(p,
		WithTools(upperTool{}),
		WithHooks(injectHook{tool: renamed}),
		WithToolWarp(tagToolWarp("w1", &log), tagToolWarp("w2", &log)),
	)

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}

	// 两个工具调用 × 两层 warp：顺序为先注册的 w1 在外层。
	want := []string{
		"enter:w1", "enter:w2", "exit:w2", "exit:w1",
		"enter:w1", "enter:w2", "exit:w2", "exit:w1",
	}
	if fmt.Sprint(log) != fmt.Sprint(want) {
		t.Fatalf("log: %v", log)
	}
	// 结果未被破坏。
	if state.Messages[2].Content != "UP:a" {
		t.Fatalf("static tool result: %q", state.Messages[2].Content)
	}
	if state.Messages[3].Content != "UP:b" {
		t.Fatalf("injected tool result: %q", state.Messages[3].Content)
	}
}

type renamedTool struct {
	inner types.Tool
	name  string
}

func (t *renamedTool) Name() string                { return t.name }
func (t *renamedTool) Description() string         { return t.inner.Description() }
func (t *renamedTool) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }
func (t *renamedTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	return t.inner.Invoke(ctx, args)
}
