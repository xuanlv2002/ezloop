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
	// 流式降级警告：Warp 链可能擦除 Stream 能力（包装者只实现了 Invoke），
	// 显式发事件而非静默变更行为。
	if a.streaming {
		if _, ok := a.provider.(provider.StreamProvider); !ok {
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
		resp, err := a.callModel(ctx, state)
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

// execToolCalls 执行本轮全部工具调用，分三段：
//  1. 判定：toolStart hooks 顺序执行（Skip/Abort 短路在此生效；hook 可阻塞等待人工决策）
//  2. 执行：放行的调用按 MaxConcurrency 并发 Invoke（单 goroutine 内 recover，工具错误不终止 loop）
//  3. 收尾：按原始顺序追加消息、顺序执行 toolEnd hooks、按序发事件
//
// 不变量：无论本轮以何种方式结束（完成/Skip/Abort/hook 报错/取消），
// 每个已写入历史的 tool_call 都有对应的 tool 结果消息——
// 发给模型的消息序列永远协议完整，持久化恢复无需理解断点。
// hook 报错或 KindAbort 会终止 loop（state.Stop 由调用方检查）。
func (a *Agent) execToolCalls(ctx context.Context, state *types.LoopState) error {
	calls := state.PendingToolCalls
	results := make([]*types.ToolResult, len(calls))
	skip := make([]bool, len(calls))

	// 判定段。hook 报错记录后不再判定/执行，但仍走收尾段补全消息。
	var hookErr error
	aborted := false
	skipResult := make([]string, len(calls)) // 首个 Skip hook 携带的结果文案
	pending := make([]int, 0, len(calls))
	for i := range calls {
		call := &calls[i]
		a.emit(state, event.EventToolStart, call)
		proceed := true

		// 运行 toolStart hooks
		for _, h := range a.toolStartHooks {
			action, err := a.runToolStartHook(h, ctx, state, call)
			if err != nil {
				hookErr = err
				break
			}
			switch action.Kind {
			case hook.KindSkip:
				skip[i] = true
				if skipResult[i] == "" { // 保留首个 Skip hook 携带的结果文案
					skipResult[i] = action.Result
				}
				proceed = false
			case hook.KindAbort:
				aborted = true
			}
			if aborted {
				break
			}
		}
		if hookErr != nil || aborted {
			break
		}
		if proceed {
			pending = append(pending, i)
		}
	}
	if aborted {
		state.Stop = true
		state.StopReason = types.StopAborted
	}

	// 执行段。工具节点与模型节点平级注入事件出口（tool warp 用 event.EmitEvent）。
	tctx := a.withEmitter(ctx, state)
	invoke := func(i int) {
		call := &calls[i]
		result := &types.ToolResult{CallID: call.ID, Name: call.Name}
		results[i] = result
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
		content, err := tool.Invoke(tctx, call.Args)
		if err != nil {
			result.Err = err
			return
		}
		result.Content = content
	}
	if !aborted && hookErr == nil {
		if a.hyper.MaxConcurrency <= 1 || len(pending) <= 1 {
			for _, i := range pending {
				invoke(i)
			}
		} else {
			sem := make(chan struct{}, a.hyper.MaxConcurrency)
			var wg sync.WaitGroup
			for _, i := range pending {
				wg.Add(1)
				go func() {
					defer wg.Done()
					sem <- struct{}{}
					defer func() { <-sem }()
					invoke(i)
				}()
			}
			wg.Wait()
		}
	}

	// 收尾段：严格按调用顺序，给每个调用补齐结果（不变量在此兑现）。
	for i := range calls {
		if results[i] == nil {
			content := skipResult[i] // Skip hook 携带的结果（拒绝理由、用户回答等）
			if content == "" {
				if skip[i] {
					content = "skipped by tool-start hook: " + calls[i].Name
				} else {
					content = "not executed: " + calls[i].Name // abort / hook 报错后未及执行
				}
			}
			results[i] = &types.ToolResult{CallID: calls[i].ID, Name: calls[i].Name, Content: content}
		}
		result := results[i]
		// 先跑 toolEnd hooks 再写消息：hook 可改写 result（如 offload 卸载大结果），
		// 改写后的内容才进入历史。
		for _, h := range a.toolEndHooks {
			if herr := a.runHook(h, "OnToolEnd", func() error { return h.OnToolEnd(ctx, state, result) }); herr != nil {
				return herr
			}
		}
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
		a.emit(state, event.EventToolEnd, result)
	}
	return hookErr
}

func (a *Agent) callModel(ctx context.Context, state *types.LoopState) (*types.ModelResponse, error) {
	req := &types.ModelRequest{Messages: state.Messages, Tools: state.Tools.List()}
	a.emit(state, event.EventModelStart, nil)

	ctx = a.withEmitter(ctx, state)

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

// withEmitter 把事件出口注入 ctx：warp 层（重试、降级、卸载、防护等）
// 拿不到 LoopState，经 ctx 发事件（event.EmitEvent）流经 OnEvent / RunAsync。
// 模型与工具两条 warp 链平级注入；工具并发执行时出口会被并发调用，
// 并发安全由使用方的回调负责（RunAsync 的 channel 出口天然安全）。
func (a *Agent) withEmitter(ctx context.Context, state *types.LoopState) context.Context {
	return event.ContextWithEmitter(ctx, func(e event.Event) {
		a.emit(state, e.Type, e.Data)
	})
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
