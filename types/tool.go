package types

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

/* Tool 是工具节点接口：schema 供模型发现，Invoke 执行（可并发，碰不到 state）。 */
type Tool interface {
	Name() string
	Description() string
	ArgsSchema() json.RawMessage
	Invoke(ctx context.Context, args json.RawMessage) (string, error)
}

/*
ToolRegistry 持有已注册工具。List 按工具名排序返回——排序是工具集的

确定函数，与注册顺序、组装路径无关（resume、重新 New、hook 注册时机变化
都不会打散），tools 序列化顺序因此跨请求稳定，前缀缓存（KV cache）才可能
命中。同名注册直接覆盖（同名 = 同一工具的新版本，幂等重注册与热加载语义）。
*/
type ToolRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
	warp  func(Tool) Tool
}

/* NewToolRegistry 创建注册表并注册初始工具。 */
func NewToolRegistry(tools ...Tool) *ToolRegistry {
	r := &ToolRegistry{tools: map[string]Tool{}}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

/*
SetWarp 设置注册拦截：之后所有 Register 的工具都会被包装（含 hook 运行时

注入的工具，如 mcp router）。已注册的不受影响。
*/
func (r *ToolRegistry) SetWarp(warp func(Tool) Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warp = warp
}

/* Register 注册工具：经 warp 壳包装后按名存入，同名覆盖。 */
func (r *ToolRegistry) Register(t Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.warp != nil {
		t = r.warp(t)
	}
	r.tools[t.Name()] = t
}

/*
Lookup 按名解析工具；找不到返回 error——模型幻觉调用会走到这里，

错误作为工具结果回传模型自纠，不终止 loop。
*/
func (r *ToolRegistry) Lookup(name string) (Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool %q not found", name)
	}
	return t, nil
}

/* List 返回按工具名排序的全部工具（进请求序列化的唯一出口）。 */
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
