package types

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

type Tool interface {
	Name() string
	Description() string
	ArgsSchema() json.RawMessage
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolWarpHandler 是工具中间件：包装工具实现重试、超时、审计、缓存等，
// 与 provider.WarpHandler 对称的节点装饰器。
type ToolWarpHandler func(Tool) Tool

// ToolWarp 用中间件链包装工具：先注册的位于最外层。
func ToolWarp(t Tool, warps ...ToolWarpHandler) Tool {
	for i := len(warps) - 1; i >= 0; i-- {
		t = warps[i](t)
	}
	return t
}

type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	warp  func(Tool) Tool
}

func NewToolRegistry(tools ...Tool) *ToolRegistry {
	r := &ToolRegistry{tools: make(map[string]Tool)}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

// SetWarp 设置注册拦截：之后所有 Register 的工具都会被包装
// （包括 hook 在运行时注入的工具，如 mcp router）。已注册的不受影响。
func (r *ToolRegistry) SetWarp(warp func(Tool) Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warp = warp
}

func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warp != nil {
		t = r.warp(t)
	}
	r.tools[t.Name()] = t
}

func (r *ToolRegistry) Lookup(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return t, nil
}

func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	return list
}
