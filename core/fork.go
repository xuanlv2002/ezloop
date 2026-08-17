package core

import (
	"context"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
)

// Fork 在隔离的消息历史上运行一次子循环（上下文隔离 fork）：复刻当前 Agent
// 的 provider / model warp / 流式 / 超参，从 seed 消息快照 + input 继续。
//
// 与 Run 的区别：
//   - 不注入 system prompt——seed 已含完整 system（含 skill 等动态注入内容），
//     再次注入会重复；
//   - 不跑任何生命周期 hook——隔离执行是纯计算，审批 / 摘要 / 会话快照等
//     横切 hook 归主循环，子循环内不递归触发；
//   - 工具由调用方传入并原样注册——继承自 state 的工具已含主循环的 tool
//     warp 链，故清空 toolWarps，不再二次包装；
//   - 事件出口走调用方给定的 onEvent，用于给子循环事件打标识（如 taskId）。
//
// 通过浅拷贝 Agent 并清空 hook 链实现：Agent 构建后只读是框架契约，拷贝
// 安全；并发调用安全——每次调用都在独立的局部拷贝上运行，不共享可变状态。
// 子循环内不再暴露 Fork 入口，单层由调用方（如 task 剔除自身工具）保证。
func (a *Agent) Fork(ctx context.Context, seed []types.Message, tools []types.Tool, input string, onEvent event.OnEvent) (*types.LoopState, error) {
	sub := *a
	sub.startHooks = nil
	sub.modelStartHooks = nil
	sub.modelEndHooks = nil
	sub.toolStartHooks = nil
	sub.toolEndHooks = nil
	sub.loopHooks = nil
	sub.endHooks = nil
	sub.tools = tools
	sub.toolWarps = nil // 传入工具已含主循环 warp 链，不再二次包装
	if onEvent != nil {
		sub.onEvent = onEvent
	}
	sub.systemPrompt = "" // seed 已含完整 system（含 skill 注入），不再重复注入

	return sub.Run(ctx, input, func(state *types.LoopState) {
		state.Messages = append(state.Messages, seed...)
	})
}
