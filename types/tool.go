package types

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

type Tool interface {
	Name() string
	Description() string
	ArgsSchema() json.RawMessage
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

// ToolRegistry 持有已注册工具。List 按工具名排序返回——排序是工具集的
// 确定函数，与注册顺序、组装路径无关（resume 恢复、重新 New、hook 注册
// 时机变化都不会打散），tools 序列化顺序因此跨请求稳定，前缀缓存
// （KV cache）才可能命中。底层 map 存储：Lookup 直查；同名注册直接
// 覆盖（同名 = 同一工具的新版本，幂等重注册与热加载的正确语义）。
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	warp  func(Tool) Tool
}

func NewToolRegistry(tools ...Tool) *ToolRegistry {
	r := &ToolRegistry{tools: map[string]Tool{}}
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
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
