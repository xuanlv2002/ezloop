// Package limit 是工具节点中间件：跨全部工具共享一个信号量，限制同一
// 时刻实际执行的工具调用数——模型一轮 fan-out N 个调用时（引擎默认全并发），
// 保护外部资源不被 N 路同时打挂（rate-limit / DB 连接 / 外呼风暴）。
//
// 闸门在 Warp(n) 调用时创建、Agent 生命周期内全局共享（跨 Run、跨 fork
// 分身都算数——保护的是外部资源，与哪轮 Run 无关）。
package limit

import (
	"context"
	"encoding/json"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
	"github.com/xuanlv2002/ezloop/warp"
)

// Warp 返回并发上限 n 的工具中间件；n < 1 视为 1。
// 经 core.WithToolWarp 挂载一次即覆盖全部工具（静态注册与 hook 运行时注入）。
func Warp(n int) warp.ToolHandler {
	if n < 1 {
		n = 1
	}
	sem := make(chan struct{}, n)
	return func(_ event.Emitter, inner types.Tool) types.Tool {
		return &limited{sem: sem, inner: inner}
	}
}

type limited struct {
	sem   chan struct{}
	inner types.Tool
}

func (t *limited) Name() string                { return t.inner.Name() }
func (t *limited) Description() string         { return t.inner.Description() }
func (t *limited) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }

func (t *limited) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	select {
	case t.sem <- struct{}{}:
		defer func() { <-t.sem }()
		return t.inner.Invoke(ctx, args)
	case <-ctx.Done(): // 排队中取消：立即让位，不阻塞当轮取消
		return "", ctx.Err()
	}
}
