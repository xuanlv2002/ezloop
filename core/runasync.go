package core

import (
	"context"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
)

// RunHandle 是 RunAsync 的异步句柄：Events 通道消费实时事件，
// Cancel 取消执行，Wait 获取最终结果。loop 结束后 Events 自动关闭。
type RunHandle struct {
	events chan event.Event
	cancel context.CancelFunc
	done   chan struct{}
	state  *types.LoopState
	err    error
}

// Events 返回事件通道（缓冲 256）。注意：channel 满时 loop 会阻塞，
// 消费方应尽快消费；审计等场景不可丢弃事件。
func (h *RunHandle) Events() <-chan event.Event { return h.events }

// Cancel 取消执行（幂等）。loop 在当前节点收尾后以 cancelled 停止。
func (h *RunHandle) Cancel() { h.cancel() }

// Wait 阻塞直到 loop 结束，返回最终状态。
func (h *RunHandle) Wait() (*types.LoopState, error) {
	<-h.done
	return h.state, h.err
}

// RunAsync 以 goroutine 驱动 loop，返回异步句柄。
// 事件经 Events 通道给出（与 WithOnEvent 同一事件流，二选一即可）。
//
//	h := agent.RunAsync(ctx, "任务")
//	defer h.Cancel()
//	for e := range h.Events() { render(e) }
//	state, err := h.Wait()
func (a *Agent) RunAsync(ctx context.Context, input string, runOpts ...RunOption) *RunHandle {
	ctx, cancel := context.WithCancel(ctx)
	events := make(chan event.Event, 256)

	// 浅拷贝 agent 并替换事件出口；NewAgent 后字段只读，拷贝安全。
	clone := *a
	clone.onEvent = func(e event.Event) { events <- e }

	h := &RunHandle{events: events, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		defer close(events)
		h.state, h.err = clone.Run(ctx, input, runOpts...)
	}()
	return h
}
