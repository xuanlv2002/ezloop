// Package testutil 提供各包测试共享的 mock：脚本式 Provider、
// 阻塞 Provider、echo 工具与响应构造器。
// 仅 module 内部可用，避免在每个测试文件里重复定义。
package testutil

import (
	"context"
	"encoding/json"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// Text 构造纯文本响应。
func Text(s string) *types.ModelResponse {
	return &types.ModelResponse{Content: s}
}

// ToolCalls 构造携带工具调用的响应。
func ToolCalls(calls ...types.ToolCall) *types.ModelResponse {
	return &types.ModelResponse{Content: "calling tools", ToolCalls: calls}
}

// Call 构造一个工具调用。
func Call(id, name string, args string) types.ToolCall {
	return types.ToolCall{ID: id, Name: name, Args: json.RawMessage(args)}
}

// ScriptedProvider 按脚本顺序返回响应，超出脚本返回空响应（loop 自然结束）。
// 同时实现 ModelProvider 与 StreamProvider，流式时逐字符发 chunk。
type ScriptedProvider struct {
	Script []*types.ModelResponse
	Calls  int
}

func Scripted(resps ...*types.ModelResponse) *ScriptedProvider {
	return &ScriptedProvider{Script: resps}
}

func (p *ScriptedProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	if p.Calls >= len(p.Script) {
		return &types.ModelResponse{}, nil
	}
	resp := p.Script[p.Calls]
	p.Calls++
	return resp, nil
}

func (p *ScriptedProvider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	resp, err := p.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, r := range resp.Content {
		if cerr := onChunk(types.ModelChunk{ContentDelta: string(r)}); cerr != nil {
			return nil, cerr
		}
	}
	return resp, nil
}

// BlockingProvider 阻塞直到 Release 关闭或 ctx 取消。
type BlockingProvider struct{ Release chan struct{} }

func NewBlocking() *BlockingProvider {
	return &BlockingProvider{Release: make(chan struct{})}
}

func (p *BlockingProvider) Invoke(ctx context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.Release:
		return &types.ModelResponse{Content: "ok"}, nil
	}
}

// EchoTool 回显参数的通用工具。
type EchoTool struct{}

func (EchoTool) Name() string                { return "echo" }
func (EchoTool) Description() string         { return "echo args" }
func (EchoTool) ArgsSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (EchoTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	return "echo: " + string(args), nil
}
