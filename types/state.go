package types

import (
	"time"

	"github.com/xuanlv2002/ezloop/event"
)

/*
LoopState 是贯穿整个 loop 的统一结构体，hook 可读写任意字段。

context.Context 按 Go 惯例作为独立参数传递，不放入 State。
*/
type LoopState struct {
	Input    string
	Messages []Message

	Tools *ToolRegistry

	Iteration     int
	MaxIterations int

	// PendingToolCalls 是本轮模型响应中待执行的调用，由引擎填充。
	PendingToolCalls []ToolCall

	LastResponse *ModelResponse
	Usage        Usage

	// ForkID 非空表示这是 fork 子循环（core.Fork）的状态：引擎发出的事件
	// 带上它，hook（如 localsession）按它分流归属；主循环为空串。
	ForkID string

	// SeedLen 是 fork 子循环携带的上下文快照长度（Messages 前 SeedLen 条
	// 为 seed）。持久化层据此剥离 seed 只存分身增量；主循环为 0。
	SeedLen int

	// Metadata 供 hook 之间共享任意数据。
	// 并发契约：除 OnToolStart / OnToolEnd（跨调用并发）外，引擎串行执行
	// hook 回调；这两个回调内写 Metadata 须自行加锁。
	Metadata map[string]any

	// Emitter 由引擎注入：hook 通过它发送自定义事件到 OnEvent 回调。
	// OnToolStart / OnToolEnd 跨调用并发，其中发事件须遵守 OnEvent 的
	// 并发契约（快速返回、并发安全）。
	Emitter event.OnEvent

	// Stop 置 true 后，当前节点收尾完毕即终止 loop。
	Stop       bool
	StopReason StopReason

	StartedAt time.Time
	EndedAt   time.Time
}

/*
EmitEvent 发送自定义事件（自动补全时间戳、迭代号与 ForkID）。

事件类型建议加命名空间前缀（如 "approve.denied"）。Emitter 未注入时空操作。
*/
func (s *LoopState) EmitEvent(typ event.EventType, data any) {
	if s.Emitter == nil {
		return
	}
	s.Emitter(event.Event{
		Type:      typ,
		Timestamp: time.Now(),
		Iteration: s.Iteration,
		ForkID:    s.ForkID,
		Data:      data,
	})
}

/* AppendMessage 追加一条消息到历史。 */
func (s *LoopState) AppendMessage(m Message) {
	s.Messages = append(s.Messages, m)
}
