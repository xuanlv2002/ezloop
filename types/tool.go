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
