// Package taskplan 提供 task_plan 工具：模型提交任务规划后中断等待
// 用户处置——执行 / 拒绝 / 按修改意见调整后重新提交。
// 实现与 approve 同构：OnToolStart 阻塞在 Decisions channel 上，
// 处置意见作为工具结果进入消息历史，由模型据此继续或修订。
package taskplan

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

// ToolName 是规划提交工具的注册名。
const ToolName = "task_plan"

// EventRequest 是规划提交事件，Data 为 *types.ToolCall（Args.plan 为规划全文）。
const EventRequest = event.EventType("taskplan.request")

// Kind 是用户对规划的处置。
type Kind int

const (
	Execute Kind = iota // 批准，按原规划执行
	Reject              // 否决，不执行
	Revise              // 按修改意见调整后重新提交
)

// Decision 是对一次规划提交的回应。CallID 留空视为回应当前请求。
type Decision struct {
	CallID string
	Kind   Kind
	// Input：Revise 时为修改意见；Reject 时可选理由；Execute 时可选补充说明。
	Input string
}

type Hook struct {
	decisions chan Decision
}

// New 创建 hook 并返回决策 channel 的发送端。
// 需同时注册 Tool() 供模型发现该工具。
// 决策必须从其他 goroutine 发送，理由同 approve.New。
func New() (*Hook, chan<- Decision) {
	ch := make(chan Decision)
	return &Hook{decisions: ch}, ch
}

func (h *Hook) Name() string { return "taskplan" }

func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name != ToolName {
		return hook.Proceed, nil
	}
	state.EmitEvent(EventRequest, call)
	for {
		select {
		case d := <-h.decisions:
			if d.CallID != "" && d.CallID != call.ID {
				continue
			}
			return hook.Skip(d.formatResult()), nil
		case <-ctx.Done():
			return hook.Skip(""), ctx.Err()
		}
	}
}

func (d Decision) formatResult() string {
	switch d.Kind {
	case Execute:
		if d.Input != "" {
			return "plan approved by user (note: " + d.Input + "), execute as planned"
		}
		return "plan approved by user, execute as planned"
	case Reject:
		if d.Input != "" {
			return "plan rejected by user: " + d.Input
		}
		return "plan rejected by user"
	default: // Revise
		return "plan needs revision, user feedback: " + d.Input
	}
}

// Tool 返回 task_plan 壳工具：仅提供 schema 供模型发现，
// 真正的"执行体"是 Hook 拦截后等用户处置，Invoke 不会被走到。
func Tool() types.Tool { return tool{} }

type tool struct{}

func (tool) Name() string { return ToolName }
func (tool) Description() string {
	return "提交任务规划等待用户处置：用户将选择执行、拒绝或给出修改意见。开始多步任务前先提交规划。"
}
func (tool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"plan":{"type":"string","description":"完整的任务规划，分步骤列出"}},"required":["plan"]}`)
}
func (tool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return "", errors.New("taskplan: hook not registered (task_plan is intercepted by taskplan.Hook)")
}
