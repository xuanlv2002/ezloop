// Package contextfix 在每次 Run 开始时修理消息历史：
// assistant 的 tool_call 若缺少对应 tool 结果消息（外部注入的残缺历史、
// 旧存档、异常中断等），自动补一条占位结果，保证发给模型的序列协议完整。
// 与引擎收尾段的不变量互补：引擎保证本轮产生的历史完整，
// 本 hook 负责历史进入引擎前完整。
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

// Fix 给缺失 tool 结果的调用补占位消息，插在所属 assistant 消息之后。
// 历史完整时原样返回。
func Fix(msgs []types.Message) []types.Message {
	answered := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Role == types.RoleTool && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	missing := false
	for _, m := range msgs {
		if m.Role != types.RoleAssistant {
			continue
		}
		for _, c := range m.ToolCalls {
			if !answered[c.ID] {
				missing = true
				break
			}
		}
	}
	if !missing {
		return msgs
	}

	fixed := make([]types.Message, 0, len(msgs)+4)
	for _, m := range msgs {
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
