/*
Package hook 定义 loop 引擎的全部扩展点，采用小接口隔离：
扩展只需实现关心的接口，NewAgent 内部类型断言归位。

并发契约：除 OnToolStart 与 OnToolEnd 外，引擎串行调用全部 hook 回调。
每个工具调用是独立单元，OnToolStart / OnToolEnd 在同一轮的多个调用之间
并发（单调用内 hooks 仍串行）——多个人工审批得以同时呈现而非排队。
实现必须并发安全：安全用法＝只读 state、state.EmitEvent、阻塞等待 channel；
不安全用法＝无锁写共享状态。hook 自建的 goroutine 中不要读写 LoopState /
Metadata——引擎对其并发访问不做保护，需要共享数据时自行加锁或只走 channel。
*/
package hook

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

/* Hook 是全部扩展的基接口。 */
type Hook interface {
	Name() string
}

/* StartHook 在 Run 开始时触发一次（组装期）：注入工具、修理历史等。 */
type StartHook interface {
	Hook
	OnStart(ctx context.Context, state *types.LoopState) error
}

/* ModelStartHook 在每次调用模型前触发；置 state.Stop = true 可提前终止。 */
type ModelStartHook interface {
	Hook
	OnModelStart(ctx context.Context, state *types.LoopState) error
}

/* ModelEndHook 在每次模型调用后触发。 */
type ModelEndHook interface {
	Hook
	OnModelEnd(ctx context.Context, state *types.LoopState) error
}

/*
Action 是 ToolStartHook 的短路控制，内嵌短路时的工具结果：

决定"放行/跳过/终止"与"跳过时模型看到什么"是同一个决策，不应分离两处。
*/
type Action struct {
	Kind   ActionKind
	Result string // Kind == KindSkip 时作为工具结果写入消息历史，空则用引擎默认文案
}

type ActionKind int

const (
	KindProceed ActionKind = iota
	KindSkip
	KindAbort
)

/* Proceed 正常执行工具。 */
var Proceed = Action{Kind: KindProceed}

/* Abort 终止整个 loop（StopReason = aborted）。 */
var Abort = Action{Kind: KindAbort}

/* Skip 跳过本次调用：result 作为工具结果写入消息历史，循环继续。 */
func Skip(result string) Action { return Action{Kind: KindSkip, Result: result} }

/* ToolStartHook 在每个工具调用前触发，是权限拦截等能力的挂载点。 */
type ToolStartHook interface {
	Hook
	OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (Action, error)
}

/*
ToolEndHook 在工具调用后、结果写入消息历史前触发：

可改写 result.Content（如 offload 卸载大结果），改写后进入历史。
*/
type ToolEndHook interface {
	Hook
	OnToolEnd(ctx context.Context, state *types.LoopState, result *types.ToolResult) error
}

/*
LoopHook 在每次迭代回边前触发：max-iteration 守卫、上下文压缩、

配置热加载（如 mcp）等横切能力的挂载点。
*/
type LoopHook interface {
	Hook
	OnLoop(ctx context.Context, state *types.LoopState) error
}

/* EndHook 无论 loop 成败都会执行（引擎 defer 语义）。 */
type EndHook interface {
	Hook
	OnEnd(ctx context.Context, state *types.LoopState) error
}
