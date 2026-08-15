package types

import "time"

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
	Metadata map[string]any

	// Stop 置 true 后，当前节点收尾完毕即终止 loop。
	Stop       bool
	StopReason StopReason

	StartedAt time.Time
	EndedAt   time.Time
}

func (s *LoopState) AppendMessage(m Message) {
	s.Messages = append(s.Messages, m)
}
