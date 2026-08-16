package event

import (
	"fmt"
	"time"
)

type EventType string

const (
	EventLoopStart    EventType = "loop_start"
	EventLoopEnd      EventType = "loop_end"
	EventModelStart   EventType = "model_start"
	EventModelChunk   EventType = "model_chunk"
	EventModelEnd     EventType = "model_end"
	EventToolStart    EventType = "tool_start"
	EventToolEnd      EventType = "tool_end"
	EventIterationEnd EventType = "iteration_end"
	EventError        EventType = "error"
	// EventStreamFallback 警告：WithStreaming(true) 但 Provider（或其 Warp 链）
	// 未实现 StreamProvider，已降级为非流式调用。
	EventStreamFallback EventType = "stream_fallback"
)

// Event 只做观察（含流式 chunk），不用于修改状态——修改状态是 Hook 的职责。
type Event struct {
	Type      EventType
	Timestamp time.Time
	Iteration int
	Data      any
}

func (e Event) String() string {
	if e.Data == nil {
		return fmt.Sprintf("[%s] iter=%d", e.Type, e.Iteration)
	}
	return fmt.Sprintf("[%s] iter=%d data=%v", e.Type, e.Iteration, e.Data)
}

type OnEvent func(Event)

// Emitter 是事件出口的最小接口面：warp 链在每次 Run 组装时由引擎注入，
// 节点内部（重试、降级、防护等）经它发事件——只观察，不碰 state。
type Emitter func(typ EventType, data any)

// Noop 供无事件消费时使用。
func Noop(Event) {}
