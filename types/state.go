package types

import (
	"time"

	"github.com/xuanlv2002/ezloop/event"
)

// LoopState 是贯穿整个 loop 的统一结构体，hook 可读写任意字段。
// context.Context 按 Go 惯例作为独立参数传递，不放入 State。
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

	// Metadata 供 hook 之间共享任意数据。
	// 并发契约：除 OnToolStart / OnToolEnd（跨调用并发）外，引擎串行执行
	// hook 回调；这两个回调内写 Metadata 须自行加锁。
	Metadata map[string]any

	// Emitter 由引擎注入：hook 通过它发送自定义事件到 OnEvent 回调。
	// hook 回调由引擎串行调用，此路径无并发；并发来自 warp 层时
	// 使用 event.EmitEvent（并发安全责任见其注释）。
	Emitter event.OnEvent

	// Stop 置 true 后，当前节点收尾完毕即终止 loop。
	Stop       bool
	StopReason StopReason

	StartedAt time.Time
	EndedAt   time.Time
}

// EmitEvent 发送自定义事件（自动补全时间戳与迭代号）。
// 事件类型建议加命名空间前缀，如 "approve.denied"，避免与引擎事件冲突。
// Emitter 未注入时为空操作。
func (s *LoopState) EmitEvent(typ event.EventType, data any) {
	if s.Emitter == nil {
		return
	}
	s.Emitter(event.Event{
		Type:      typ,
		Timestamp: time.Now(),
		Iteration: s.Iteration,
		Data:      data,
	})
}

func (s *LoopState) AppendMessage(m Message) {
	s.Messages = append(s.Messages, m)
}
