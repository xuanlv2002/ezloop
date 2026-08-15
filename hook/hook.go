// Package hook 定义 loop 引擎的全部扩展点。
// 采用小接口隔离：扩展只需实现关心的接口，NewAgent 内部类型断言归位。
package hook

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

type Hook interface {
	Name() string
}

type StartHook interface {
	Hook
	OnStart(ctx context.Context, state *types.LoopState) error
}

type ModelStartHook interface {
	Hook
	// OnModelStart 在每次调用模型前触发；置 state.Stop = true 可提前终止。
	OnModelStart(ctx context.Context, state *types.LoopState) error
}

type ModelEndHook interface {
	Hook
	OnModelEnd(ctx context.Context, state *types.LoopState) error
}

// Action 是 ToolStartHook 的短路控制。
type Action int

const (
	// ActionProceed 正常执行工具。
	ActionProceed Action = iota
	// ActionSkip 跳过本次调用（结果标记为 skipped，循环继续）。
	ActionSkip
	// ActionAbort 终止整个 loop（StopReason = aborted）。
	ActionAbort
)

type ToolStartHook interface {
	Hook
	// OnToolStart 在每个工具调用前触发，是权限拦截等能力的挂载点。
	OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (Action, error)
}

type ToolEndHook interface {
	Hook
	OnToolEnd(ctx context.Context, state *types.LoopState, result *types.ToolResult) error
}

// LoopHook 在每次迭代回边前触发：max-iteration 守卫、上下文压缩、
// 配置热加载（如 mcp）等横切能力的挂载点。
type LoopHook interface {
	Hook
	OnLoop(ctx context.Context, state *types.LoopState) error
}

type EndHook interface {
	Hook
	// OnEnd 无论 loop 成败都会执行（引擎 defer 语义）。
	OnEnd(ctx context.Context, state *types.LoopState) error
}
