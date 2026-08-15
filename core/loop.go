package core

import (
	"context"
	"fmt"
	"time"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// Run 驱动整个 loop：model ↔ tool 循环，直到模型不再发起 tool call、
// 达到 MaxIterations、hook 置 Stop 或出错。EndHook 无论成败都会执行。
func (a *Agent) Run(ctx context.Context, input string) (state *types.LoopState, err error) {
	state = &types.LoopState{
		Input:         input,
		Tools:         types.NewToolRegistry(),
		MaxIterations: a.maxIterations,
		Metadata:      make(map[string]any),
		StartedAt:     time.Now(),
	}
	// 先挂 tool warp，再注册静态工具，保证两者都会被包装。
	if len(a.toolWarps) > 0 {
		state.Tools.SetWarp(func(t types.Tool) types.Tool {
			return types.ToolWarp(t, a.toolWarps...)
		})
	}
	for _, t := range a.tools {
		state.Tools.Register(t)
	}
	a.emit(state, event.EventLoopStart, input)

	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ezloop: panic: %v", r)
			state.StopReason = types.StopError
			a.emit(state, event.EventError, err)
		}
		if endErr := a.runEndHooks(ctx, state); endErr != nil && err == nil {
			err = endErr
		}
		state.EndedAt = time.Now()
		a.emit(state, event.EventLoopEnd, state.StopReason)
	}()

	state.AppendMessage(types.Message{Role: types.RoleUser, Content: input})

	for _, h := range a.startHooks {
		if err = h.OnStart(ctx, state); err != nil {
			return state, a.fail(state, err)
		}
	}

	for state.Iteration < state.MaxIterations && !state.Stop {
		state.Iteration++

		for _, h := range a.modelStartHooks {
			if err = h.OnModelStart(ctx, state); err != nil {
				return state, a.fail(state, err)
			}
		}
		if state.Stop {
			break
		}

		resp, err := a.callModel(ctx, state)
		if err != nil {
			return state, a.fail(state, err)
		}
		state.LastResponse = resp
		state.Usage.Add(resp.Usage)
		state.AppendMessage(types.Message{
			Role:      types.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		a.emit(state, event.EventModelEnd, resp)

		for _, h := range a.modelEndHooks {
			if err = h.OnModelEnd(ctx, state); err != nil {
				return state, a.fail(state, err)
			}
		}
		if state.Stop {
			break
		}

		if len(resp.ToolCalls) == 0 {
			state.StopReason = types.StopCompleted
			break
		}
		state.PendingToolCalls = resp.ToolCalls

		for i := range state.PendingToolCalls {
			call := state.PendingToolCalls[i]
			abort, err := a.execToolCall(ctx, state, &call)
			if err != nil {
				return state, a.fail(state, err)
			}
			if abort {
				state.Stop = true
				break
			}
		}
		if state.Stop {
			break
		}

		for _, h := range a.loopHooks {
			if err = h.OnLoop(ctx, state); err != nil {
				return state, a.fail(state, err)
			}
		}
		a.emit(state, event.EventIterationEnd, state.Iteration)
	}

	if state.StopReason == "" {
		if state.Stop {
			state.StopReason = types.StopAborted
		} else {
			state.StopReason = types.StopMaxIteration
		}
	}
	return state, nil
}

// execToolCall 执行单次工具调用。
// 工具不存在或执行失败不终止 loop，而是作为错误结果回传模型供其自纠；
// 只有 hook 报错或返回 ActionAbort 才会终止。
func (a *Agent) execToolCall(ctx context.Context, state *types.LoopState, call *types.ToolCall) (abort bool, err error) {
	a.emit(state, event.EventToolStart, call)

	skipped := false
	for _, h := range a.toolStartHooks {
		action, herr := h.OnToolStart(ctx, state, call)
		if herr != nil {
			return false, herr
		}
		switch action {
		case hook.ActionSkip:
			skipped = true
		case hook.ActionAbort:
			return true, nil
		}
	}

	result := &types.ToolResult{CallID: call.ID, Name: call.Name}
	switch {
	case skipped:
		result.Content = "skipped by tool-start hook"
	default:
		tool, lerr := state.Tools.Lookup(call.Name)
		if lerr != nil {
			result.Err = lerr
			break
		}
		content, ierr := tool.Invoke(ctx, call.Args)
		if ierr != nil {
			result.Err = ierr
		} else {
			result.Content = content
		}
	}

	errText := ""
	if result.Err != nil {
		errText = result.Err.Error()
	}
	state.AppendMessage(types.Message{
		Role:       types.RoleTool,
		ToolCallID: call.ID,
		Content:    result.Content,
		Err:        errText,
	})

	for _, h := range a.toolEndHooks {
		if herr := h.OnToolEnd(ctx, state, result); herr != nil {
			return false, herr
		}
	}
	a.emit(state, event.EventToolEnd, result)
	return false, nil
}

func (a *Agent) callModel(ctx context.Context, state *types.LoopState) (*types.ModelResponse, error) {
	req := &types.ModelRequest{Messages: state.Messages, Tools: state.Tools.List()}
	a.emit(state, event.EventModelStart, nil)

	if a.streaming {
		if sp, ok := a.provider.(provider.StreamProvider); ok {
			return sp.Stream(ctx, req, func(c types.ModelChunk) error {
				a.emit(state, event.EventModelChunk, c.ContentDelta)
				return nil
			})
		}
	}
	return a.provider.Invoke(ctx, req)
}

func (a *Agent) runEndHooks(ctx context.Context, state *types.LoopState) error {
	for _, h := range a.endHooks {
		if err := h.OnEnd(ctx, state); err != nil {
			a.emit(state, event.EventError, fmt.Errorf("end hook %s: %w", h.Name(), err))
			return err
		}
	}
	return nil
}

func (a *Agent) fail(state *types.LoopState, err error) error {
	state.Stop = true
	state.StopReason = types.StopError
	a.emit(state, event.EventError, err)
	return err
}

func (a *Agent) emit(state *types.LoopState, typ event.EventType, data any) {
	if a.onEvent == nil {
		return
	}
	a.onEvent(event.Event{
		Type:      typ,
		Timestamp: time.Now(),
		Iteration: state.Iteration,
		Data:      data,
	})
}
