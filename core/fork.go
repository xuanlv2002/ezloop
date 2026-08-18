package core

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

// Fork 以"并行分身"语义运行一次子循环：复刻当前 Agent 的一切——provider /
// model warp / 流式 / 超参 / 全部运行期 hook（审批、交互、摘要、持久化……），
// 从 seed 消息快照 + input 继续，forkID 是子循环的身份标识。
//
// Fork 是引擎的通用派生原语，不隶属任何扩展：ext/hook/task 是第一个
// 使用者，任何"以当前自我为模板跑隔离循环"的场景（评审、假设分支、
// 重跑验证……）都可复用。"复刻 Agent"只能在 core 内完成——startHooks /
// toolWarps / systemPrompt 是私有字段，扩展层无法从外部跳过组装期。
//
// 与 Run 的区别仅两处：
//   - startHooks 不重跑（置 nil）：它们是组装期 hook（skill 注 system、
//     mcp/task 注册工具），产物已在 seed 与传入工具中，重跑必重复注入；
//     运行期 hook（model/tool/loop/end）全部继承——分身与本体行为一致，
//     审批照拦、可问用户、session 照存。
//   - systemPrompt 置空、toolWarps 置 nil：seed 已含完整 system（含 skill
//     等动态注入内容），传入工具已含主循环 warp 壳，注入或包装都会重复。
//
// 单层保证：startHooks 不重跑，task 工具不会在子循环注册，且 task hook
// 对 ForkID 非空的子循环拒绝再次 fork（见 ext/hook/task）。
//
// 通过浅拷贝 Agent 实现复刻：Agent 构建后只读是框架契约，拷贝安全；并发
// 调用安全——每次调用在独立的局部拷贝上运行。已知限制：继承工具的 warp
// 壳持有主循环 emitter，壳内部事件（重试、降级）不带 forkID（引擎级事件
// 经子 state 发出，全部带标）。
func (a *Agent) Fork(ctx context.Context, forkID string, seed []types.Message, tools []types.Tool, input string) (*types.LoopState, error) {
	sub := *a
	sub.startHooks = nil
	sub.tools = tools
	sub.toolWarps = nil
	sub.systemPrompt = ""

	// seed 深拷贝：子循环 append 不改写父消息底层数组（并行 fork 安全）。
	seedCopy := append(make([]types.Message, 0, len(seed)), seed...)

	return sub.Run(ctx, input, func(state *types.LoopState) {
		state.ForkID = forkID
		state.SeedLen = len(seedCopy)
		state.Messages = append(state.Messages, seedCopy...)
	})
}
