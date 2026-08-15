// ezloop 完整示例：mock 模型 + mcp.Hook + 自定义日志 hook，
// 演示模型通过 mcp_router 自发现并调用 MCP 工具的完整 loop。
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"

	"github.com/xuanlv2002/ezloop/ext/hook/mcp"
)

// scriptedProvider 模拟一个会使用工具的模型。
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

// logHook 是用户自定义扩展：实现哪些接口，就挂载到哪些节点。
type logHook struct{}

func (logHook) Name() string { return "log" }

func (logHook) OnModelEnd(_ context.Context, s *types.LoopState) error {
	fmt.Printf("[log] iteration %d: model responded (tool calls: %d)\n", s.Iteration, len(s.LastResponse.ToolCalls))
	return nil
}

func (logHook) OnToolEnd(_ context.Context, _ *types.LoopState, r *types.ToolResult) error {
	fmt.Printf("[log] tool %s done -> %.60s\n", r.Name, r.Content)
	return nil
}

func main() {
	// 一个内存版 MCP server：提供 read 工具。
	cfg := mcp.Config{Servers: []mcp.ServerConfig{
		{
			Name: "fs",
			Factory: func(mcp.ServerConfig) (mcp.Client, error) {
				return &fsClient{}, nil
			},
		},
	}}

	provider := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "我先看看有哪些 MCP 工具", ToolCalls: []types.ToolCall{
			{ID: "c1", Name: mcp.RouterToolName, Args: json.RawMessage(`{"action":"list_tools"}`)},
		}},
		{Content: "调用 read 工具", ToolCalls: []types.ToolCall{
			{ID: "c2", Name: mcp.RouterToolName, Args: json.RawMessage(`{"action":"call_tool","server":"fs","tool":"read","args":{"path":"hello.txt"}}`)},
		}},
		{Content: "完成：文件内容是 hello from mcp"},
	}}

	agent := core.NewAgent(provider,
		core.WithHooks(mcp.NewHook(cfg), logHook{}),
		core.WithOnEvent(func(e event.Event) { fmt.Println("[event]", e) }),
	)

	state, err := agent.Run(context.Background(), "读取 hello.txt")
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Printf("\nstop_reason=%s iterations=%d usage=%+v\n",
		state.StopReason, state.Iteration, state.Usage)
	fmt.Println("final:", state.LastResponse.Content)
}

type fsClient struct{}

func (c *fsClient) ListTools(_ context.Context) ([]mcp.ToolDef, error) {
	return []mcp.ToolDef{
		{Name: "read", Description: "read a file", ArgsSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)},
	}, nil
}

func (c *fsClient) CallTool(_ context.Context, name string, args json.RawMessage) (string, error) {
	if name != "read" {
		return "", fmt.Errorf("unknown tool %s", name)
	}
	var in struct{ Path string }
	_ = json.Unmarshal(args, &in)
	return "hello from mcp (" + in.Path + ")", nil
}
