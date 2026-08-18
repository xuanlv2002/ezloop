package event

import (
	"fmt"
	"time"
)

type EventType string

const (
	EventLoopStart  EventType = "loop_start"
	EventLoopEnd    EventType = "loop_end"
	EventModelStart EventType = "model_start"
	EventModelChunk EventType = "model_chunk"
	EventModelEnd   EventType = "model_end"
	// EventReasoningChunk 流式思考过程增量（推理模型的 reasoning_content），
	// data 为 string，与 EventModelChunk 分开透出。
	EventReasoningChunk EventType = "reasoning_chunk"
	EventToolStart      EventType = "tool_start"
	EventToolEnd        EventType = "tool_end"
	EventIterationEnd   EventType = "iteration_end"
	EventError          EventType = "error"
	// EventStreamFallback 警告：WithStreaming(true) 但 Provider（或其 Warp 链）
	// 未实现 StreamProvider，已降级为非流式调用。
	EventStreamFallback EventType = "stream_fallback"
)

// Event 只做观察（含流式 chunk），不用于修改状态——修改状态是 Hook 的职责。
type Event struct {
	Type      EventType
	Timestamp time.Time
	Iteration int
	// TaskID 标识事件所属的 fork 子循环（core.Fork）；主循环事件为空串。
	// 工具并发、fork 也并发，消费方以 TaskID 区分同一时刻多个 fork 的事件归属。
	TaskID string
	Data   any
}

func (e Event) String() string {
	task := ""
	if e.TaskID != "" {
		task = " task=" + e.TaskID
	}
	if e.Data == nil {
		return fmt.Sprintf("[%s]%s iter=%d", e.Type, task, e.Iteration)
	}
	return fmt.Sprintf("[%s]%s iter=%d data=%v", e.Type, task, e.Iteration, e.Data)
}

type OnEvent func(Event)

// Emitter 是事件出口的最小接口面：warp 链在每次 Run 组装时由引擎注入，
// 节点内部（重试、降级、防护等）经它发事件——只观察，不碰 state。
type Emitter func(typ EventType, data any)

// Noop 供无事件消费时使用。
func Noop(Event) {}
