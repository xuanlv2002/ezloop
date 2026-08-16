// Package contextfix 在每次 Run 开始时修理消息历史，双向修补：
// 补缺 —— assistant 的 tool_call 缺少对应 tool 结果时补占位消息；
// 删孤 —— tool 结果消息对应的 assistant 调用已丢失时删除（保留会被 API 拒绝）。
// 与引擎收尾段的不变量互补：引擎保证本轮产生的历史完整，
// 本 hook 保证历史进入引擎前完整（外部注入的残缺历史、旧存档、异常中断等）。
package contextfix

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

// Placeholder 是缺失结果的占位文案，模型据此得知该调用没有可用的执行结果。
const Placeholder = "(missing tool result)"

type Hook struct{}

func New() *Hook { return &Hook{} }

func (Hook) Name() string { return "contextfix" }

func (Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Messages = Fix(state.Messages)
	return nil
}

// Fix 双向修理历史；历史完整时原样返回（不新建切片）。
func Fix(msgs []types.Message) []types.Message {
	called := make(map[string]bool, len(msgs)) // assistant 发起过的调用
	answered := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == types.RoleAssistant {
			for _, c := range m.ToolCalls {
				called[c.ID] = true
			}
		}
		if m.Role == types.RoleTool && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	missing, orphan := false, false
	for _, m := range msgs {
		if m.Role == types.RoleTool && m.ToolCallID != "" && !called[m.ToolCallID] {
			orphan = true
		}
		if m.Role == types.RoleAssistant {
			for _, c := range m.ToolCalls {
				if !answered[c.ID] {
					missing = true
				}
			}
		}
	}
	if !missing && !orphan {
		return msgs
	}

	fixed := make([]types.Message, 0, len(msgs)+4)
	for _, m := range msgs {
		if m.Role == types.RoleTool && m.ToolCallID != "" && !called[m.ToolCallID] {
			continue // 孤儿 tool：对应 assistant 已丢失，删除
		}
		fixed = append(fixed, m)
		if m.Role != types.RoleAssistant || len(m.ToolCalls) == 0 {
			continue
		}
		for _, c := range m.ToolCalls {
			if answered[c.ID] {
				continue
			}
			fixed = append(fixed, types.Message{
				Role:       types.RoleTool,
				ToolCallID: c.ID,
				Content:    Placeholder,
			})
			answered[c.ID] = true
		}
	}
	return fixed
}
