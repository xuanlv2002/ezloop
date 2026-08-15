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
// 消息序列构造顺序：system 提示词 → WithHistory 历史 → 本次 input，
// 随后 startHook 可继续注入（如 skill）。
func (a *Agent) Run(ctx context.Context, input string, runOpts ...RunOption) (state *types.LoopState, err error) {
	state = &types.LoopState{
		Input:         input,
		Tools:         types.NewToolRegistry(),
		MaxIterations: a.maxIterations,
		Metadata:      make(map[string]any),
		StartedAt:     time.Now(),
		Emitter:       a.onEvent,
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
	if a.systemPrompt != "" {
		state.Messages = append([]types.Message{{
			Role:    types.RoleSystem,
			Content: a.systemPrompt,
		}}, state.Messages...)
	}
	for _, opt := range runOpts {
		opt(state)
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
		if err = a.runHook(h, "OnStart", func() error { return h.OnStart(ctx, state) }); err != nil {
			return state, a.fail(state, err)
		}
	}

	for state.Iteration < state.MaxIterations && !state.Stop {
		state.Iteration++

		for _, h := range a.modelStartHooks {
			if err = a.runHook(h, "OnModelStart", func() error { return h.OnModelStart(ctx, state) }); err != nil {
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
			if err = a.runHook(h, "OnModelEnd", func() error { return h.OnModelEnd(ctx, state) }); err != nil {
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
			if err = a.runHook(h, "OnLoop", func() error { return h.OnLoop(ctx, state) }); err != nil {
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
		action, herr := a.runToolStartHook(h, ctx, state, call)
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
		// 携带调用信息，模型在下一轮次可准确重发同样的调用（轮次式审批场景）。
		result.Content = fmt.Sprintf("skipped by tool-start hook: %s(%s)", call.Name, string(call.Args))
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
		if herr := a.runHook(h, "OnToolEnd", func() error { return h.OnToolEnd(ctx, state, result) }); herr != nil {
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
		if err := a.runHook(h, "OnEnd", func() error { return h.OnEnd(ctx, state) }); err != nil {
			a.emit(state, event.EventError, err)
			return err
		}
	}
	return nil
}

// runHook 是引擎对每次 hook 调用的标准包裹：
// panic 恢复为 error，error 附带 hook 名，单个扩展的崩溃不会炸掉 loop。
func (a *Agent) runHook(h hook.Hook, phase string, fn func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("hook %q panicked in %s: %v", h.Name(), phase, r)
		}
	}()
	if err := fn(); err != nil {
		return fmt.Errorf("hook %q: %w", h.Name(), err)
	}
	return nil
}

func (a *Agent) runToolStartHook(h hook.ToolStartHook, ctx context.Context, state *types.LoopState, call *types.ToolCall) (action hook.Action, err error) {
	defer func() {
		if r := recover(); r != nil {
			action, err = hook.ActionProceed, fmt.Errorf("hook %q panicked in OnToolStart: %v", h.Name(), r)
		}
	}()
	action, err = h.OnToolStart(ctx, state, call)
	if err != nil {
		err = fmt.Errorf("hook %q: %w", h.Name(), err)
	}
	return action, err
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
