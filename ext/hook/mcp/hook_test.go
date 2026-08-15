package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
)

// scriptedProvider 按脚本返回响应，驱动完整 loop。
type scriptedProvider struct {
	script []*types.ModelResponse
	calls  int
}

func (p *scriptedProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	if p.calls >= len(p.script) {
		return &types.ModelResponse{}, nil
	}
	resp := p.script[p.calls]
	p.calls++
	return resp, nil
}

func TestHookFullLoop(t *testing.T) {
	reloadCount := 0
	cfg := testCfg()
	cfg.Reload = func(context.Context) ([]ServerConfig, error) {
		reloadCount++
		return cfg.Servers, nil
	}

	p := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "let me discover tools", ToolCalls: []types.ToolCall{
			{ID: "c1", Name: RouterToolName, Args: json.RawMessage(`{"action":"list_tools"}`)},
		}},
		{Content: "now call it", ToolCalls: []types.ToolCall{
			{ID: "c2", Name: RouterToolName, Args: json.RawMessage(`{"action":"call_tool","server":"fs","tool":"read","args":{"path":"x"}}`)},
		}},
		{Content: "all done"},
	}}

	var events []event.EventType
	a := core.NewAgent(p,
		core.WithHooks(NewHook(cfg)),
		core.WithOnEvent(func(e event.Event) { events = append(events, e.Type) }),
	)

	state, err := a.Run(context.Background(), "use mcp")
	if err != nil {
		t.Fatalf("run err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("want completed, got %s", state.StopReason)
	}

	// 第二条 tool 消息应是 mock 调用结果。
	toolMsgs := 0
	for _, m := range state.Messages {
		if m.Role == types.RoleTool {
			toolMsgs++
		}
	}
	if toolMsgs != 2 {
		t.Fatalf("want 2 tool results, got %d", toolMsgs)
	}
	if state.Messages[4].Content != `read({"path":"x"})` {
		t.Fatalf("bad mcp result: %s", state.Messages[4].Content)
	}
	if reloadCount != 2 {
		t.Fatalf("want 2 config reloads (one per loop-back), got %d", reloadCount)
	}
	_ = events
}
