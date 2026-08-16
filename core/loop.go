package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
	"github.com/xuanlv2002/ezloop/warp"
)

// Run 驱动整个 loop：model ↔ tool 循环，直到模型不再发起 tool call、
// 达到 MaxIterations、hook 置 Stop 或出错。EndHook 无论成败都会执行。
// 消息序列构造顺序：system 提示词 → WithHistory 历史 → 本次 input，
// 随后 startHook 可继续注入（如 skill）。
func (a *Agent) Run(ctx context.Context, input string, runOpts ...RunOption) (state *types.LoopState, err error) {
	// 初始化状态
	state = &types.LoopState{
		Input:         input,
		Tools:         types.NewToolRegistry(),
		MaxIterations: a.hyper.MaxIterations,
		Metadata:      make(map[string]any),
		StartedAt:     time.Now(),
		Emitter:       a.onEvent,
	}
	// per-Run 事件出口与 warp 组装：模型/工具两条链注入同一 Emitter，
	// warp 实例 per-Run 独立（状态不跨 Run 共享）。
	em := event.Emitter(func(typ event.EventType, data any) {
		a.emit(state, typ, data)
	})
	// 先挂 tool warp，再注册静态工具，保证两者都会被包装。
	if len(a.toolWarps) > 0 {
		state.Tools.SetWarp(func(t types.Tool) types.Tool {
			return warp.Tool(em, t, a.toolWarps...)
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

	model := a.provider
	if len(a.modelWarps) > 0 {
		model = warp.Model(em, a.provider, a.modelWarps...)
	}
	// 流式降级警告：Warp 链可能擦除 Stream 能力（包装者只实现了 Invoke），
	// 显式发事件而非静默变更行为。
	if a.streaming {
		if _, ok := model.(provider.StreamProvider); !ok {
			a.emit(state, event.EventStreamFallback,
				"streaming requested but provider does not implement StreamProvider; falling back to Invoke")
		}
	}

	// 捕获 panic，保证 EndHook 执行。
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ezloop: panic: %v", r)
			if ctx.Err() != nil {
				state.StopReason = types.StopCancelled
			} else {
				state.StopReason = types.StopError
				// 发送错误事件，便于外部日志记录。
				a.emit(state, event.EventError, err)
			}
		}

		// 运行结束，执行 EndHook。用脱离取消的 ctx：
		// 收尾清理（关连接、写快照）不应随请求取消而失败。
		if endErr := a.runEndHooks(context.WithoutCancel(ctx), state); endErr != nil && err == nil {
			err = endErr
		}
		state.EndedAt = time.Now()
		a.emit(state, event.EventLoopEnd, state.StopReason)
	}()

	state.AppendMessage(types.Message{Role: types.RoleUser, Content: input})

	// 运行 startHook
	for _, h := range a.startHooks {
		if err = a.runHook(h, "OnStart", func() error { return h.OnStart(ctx, state) }); err != nil {
			return state, a.fail(ctx, state, err)
		}
	}

	for state.Iteration < state.MaxIterations && !state.Stop {
		if ctx.Err() != nil {
			state.StopReason = types.StopCancelled
			return state, ctx.Err()
		}
		state.Iteration++

		// 运行 modelStartHook
		for _, h := range a.modelStartHooks {
			if err = a.runHook(h, "OnModelStart", func() error { return h.OnModelStart(ctx, state) }); err != nil {
				return state, a.fail(ctx, state, err)
			}
		}
		if state.Stop {
			break
		}

		// 模型call
		resp, err := a.callModel(ctx, model, state)
		if err != nil {
			return state, a.fail(ctx, state, err)
		}
		state.LastResponse = resp
		state.Usage.Add(resp.Usage)

		state.AppendMessage(types.Message{
			Role:      types.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})
		a.emit(state, event.EventModelEnd, resp)

		// 运行 modelEndHook
		for _, h := range a.modelEndHooks {
			if err = a.runHook(h, "OnModelEnd", func() error { return h.OnModelEnd(ctx, state) }); err != nil {
				return state, a.fail(ctx, state, err)
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

		// 工具调用
		if err = a.execToolCalls(ctx, state); err != nil {
			return state, a.fail(ctx, state, err)
		}
		if state.Stop {
			break
		}
		// 运行 loop hooks
		for _, h := range a.loopHooks {
			if err = a.runHook(h, "OnLoop", func() error { return h.OnLoop(ctx, state) }); err != nil {
				return state, a.fail(ctx, state, err)
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

// execToolCalls 执行本轮全部工具调用：
// 每个调用是独立单元——toolStart 判定 → warp 壳内执行 → toolEnd 后处理，
// 整链跟随调用并发（SerialTools 时串行）；tool_start/tool_end 事件随调用
// 即时发出（到达顺序不保证，消费方以 CallID 关联）；全部完成后按原始
// 顺序汇总写入消息历史。多个人工审批同时呈现而非排队逐个等；
// 并发数量不可配（调用数即并发数，需要限流在 tool warp 内实现）。
//
// 不变量：无论本轮以何种方式结束（完成/Skip/Abort/hook 报错/取消），
// 每个已写入历史的 tool_call 都有对应的 tool 结果消息——
// 发给模型的消息序列永远协议完整，持久化恢复无需理解断点。
// 任一 Abort/hook 报错联动取消其余调用，未完成的按占位结果补全。
func (a *Agent) execToolCalls(ctx context.Context, state *types.LoopState) error {
	calls := state.PendingToolCalls
	results := make([]*types.ToolResult, len(calls))

	var (
		mu      sync.Mutex
		hookErr error
		aborted bool
	)
	callCtx, cancelCalls := context.WithCancel(ctx)
	defer cancelCalls()

	process := func(i int) {
		call := &calls[i]
		result := &types.ToolResult{CallID: call.ID, Name: call.Name}
		// 事件随调用即时发出（并发、到达顺序不保证）——
		// 消费方以 CallID 关联调用，顺序不是可靠性的来源。
		a.emit(state, event.EventToolStart, call)

		// 记录终止原因并联动取消其余调用；已有原因时后续错误多为联动取消，忽略。
		fail := func(err error) {
			mu.Lock()
			defer mu.Unlock()
			switch {
			case aborted || hookErr != nil:
			case ctx.Err() != nil:
				hookErr = err // 外部取消
			default:
				hookErr = err
				cancelCalls()
			}
		}

		// 判定：toolStart hooks（单调用内串行，Skip 保留首个结果文案）。
		proceed := true
		for _, h := range a.toolStartHooks {
			action, err := a.runToolStartHook(h, callCtx, state, call)
			if err != nil {
				fail(err)
				return
			}
			switch action.Kind {
			case hook.KindSkip:
				proceed = false
				if result.Content == "" {
					result.Content = action.Result
				}
			case hook.KindAbort:
				mu.Lock()
				aborted = true
				mu.Unlock()
				cancelCalls()
				return
			}
		}

		// 执行：warp 壳内 Invoke（工具错误不终止 loop，回传模型自纠）。
		if proceed {
			func() {
				defer func() {
					if r := recover(); r != nil {
						result.Err = fmt.Errorf("tool %q panicked: %v", call.Name, r)
					}
				}()
				tool, err := state.Tools.Lookup(call.Name)
				if err != nil {
					result.Err = err
					return
				}
				content, err := tool.Invoke(callCtx, call.Args)
				if err != nil {
					result.Err = err
					return
				}
				result.Content = content
			}()
		} else if result.Content == "" {
			result.Content = "skipped by tool-start hook: " + call.Name
		}

		// 后处理：toolEnd hooks（与调用绑定、随调用并发，可改写 result）。
		for _, h := range a.toolEndHooks {
			if herr := a.runHook(h, "OnToolEnd", func() error { return h.OnToolEnd(callCtx, state, result) }); herr != nil {
				fail(herr)
				return
			}
		}
		a.emit(state, event.EventToolEnd, result)
		results[i] = result
	}

	if a.hyper.SerialTools {
		for i := range calls {
			process(i)
			if hookErr != nil || aborted {
				break
			}
		}
	} else {
		var wg sync.WaitGroup
		for i := range calls {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				process(i)
			}(i)
		}
		wg.Wait()
	}

	// 汇总：严格按调用顺序写消息（消息历史的确定性在此兑现；
	// 已完成调用的事件已随调用即时发出，未完成的在此补发，保持事件成对）。
	for i := range calls {
		if results[i] == nil { // abort / hook 报错后未完成的调用
			results[i] = &types.ToolResult{
				CallID:  calls[i].ID,
				Name:    calls[i].Name,
				Content: "not executed: " + calls[i].Name,
			}
			a.emit(state, event.EventToolEnd, results[i])
		}
		result := results[i]
		errText := ""
		if result.Err != nil {
			errText = result.Err.Error()
		}
		state.AppendMessage(types.Message{
			Role:       types.RoleTool,
			ToolCallID: calls[i].ID,
			Content:    result.Content,
			Err:        errText,
		})
	}
	if aborted {
		state.Stop = true
		state.StopReason = types.StopAborted
	}
	return hookErr
}

func (a *Agent) callModel(ctx context.Context, model provider.ModelProvider, state *types.LoopState) (*types.ModelResponse, error) {
	req := &types.ModelRequest{Messages: state.Messages, Tools: state.Tools.List()}
	a.emit(state, event.EventModelStart, nil)

	if a.streaming {
		if sp, ok := model.(provider.StreamProvider); ok {
			return sp.Stream(ctx, req, func(c types.ModelChunk) error {
				a.emit(state, event.EventModelChunk, c.ContentDelta)
				return nil
			})
		}
	}
	return model.Invoke(ctx, req)
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
			action, err = hook.Proceed, fmt.Errorf("hook %q panicked in OnToolStart: %v", h.Name(), r)
		}
	}()
	action, err = h.OnToolStart(ctx, state, call)
	if err != nil {
		err = fmt.Errorf("hook %q: %w", h.Name(), err)
	}
	return action, err
}

// fail 是所有错误路径的统一出口：取消/超时归类 cancelled（不发错误事件），
// 其余归类 error。hook 因 ctx 取消而报错同样走这里，归类保持一致。
func (a *Agent) fail(ctx context.Context, state *types.LoopState, err error) error {
	state.Stop = true
	if ctx.Err() != nil {
		state.StopReason = types.StopCancelled
		return err
	}
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
