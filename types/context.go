package types

import (
	"context"

	"github.com/xuanlv2002/ezloop/event"
)

/*
ToolInvocation 是工具执行期的调用信息与事件出口，由引擎在 Invoke 前
注入 ctx。工具据此知道自己这次调用的 CallID（可与 tool_start/tool_end
关联），并可在执行中经 Emit 发观察事件（如流式输出）——事件自带
iteration/forkID，与引擎事件同一通道。
*/
type ToolInvocation struct {
	Call *ToolCall
	Emit event.Emitter
}

type toolInvocationKey struct{}

/* WithToolInvocation 把本次调用注入 ctx（引擎内部使用；工具侧只读）。 */
func WithToolInvocation(ctx context.Context, inv *ToolInvocation) context.Context {
	return context.WithValue(ctx, toolInvocationKey{}, inv)
}

/* ToolInvocationFromContext 取回本次调用信息；无（直接 Invoke/测试）时返回 nil。 */
func ToolInvocationFromContext(ctx context.Context) *ToolInvocation {
	inv, _ := ctx.Value(toolInvocationKey{}).(*ToolInvocation)
	return inv
}

/* EmitEvent 是工具侧发观察事件的入口：无调用上下文时静默丢弃。 */
func EmitEvent(ctx context.Context, typ event.EventType, data any) {
	if inv := ToolInvocationFromContext(ctx); inv != nil && inv.Emit != nil {
		inv.Emit(typ, data)
	}
}
