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
// （KV cache）才可能命中。工具量级是个位数到十几个，线性查找即可。
type ToolRegistry struct {
	mu    sync.RWMutex
	tools []Tool
	warp  func(Tool) Tool
}

func NewToolRegistry(tools ...Tool) *ToolRegistry {
	r := &ToolRegistry{}
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
	for i, old := range r.tools {
		if old.Name() == t.Name() {
			r.tools[i] = t // 同名覆盖：原位替换，保序
			return
		}
	}
	r.tools = append(r.tools, t)
}

func (r *ToolRegistry) Lookup(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, t := range r.tools {
		if t.Name() == name {
			return t, nil
		}
	}
	return nil, fmt.Errorf("tool %q not found", name)
}

func (r *ToolRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := append([]Tool(nil), r.tools...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}
