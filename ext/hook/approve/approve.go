// Package approve 提供工具调用审批：每次工具执行前询问 Approver，
// 拒绝时按配置跳过（Skip，默认）或终止（Abort）。
package approve

import (
	"context"

	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

// Approver 返回 false 表示拒绝执行该工具调用。
type Approver func(ctx context.Context, call *types.ToolCall) (bool, error)

type Hook struct {
	approver Approver
	onDeny   hook.Action
}

func New(approver Approver, onDeny ...hook.Action) *Hook {
	h := &Hook{approver: approver, onDeny: hook.ActionSkip}
	for _, a := range onDeny {
		if a == hook.ActionAbort {
			h.onDeny = hook.ActionAbort
		}
	}
	return h
}

func (h *Hook) Name() string { return "approve" }

func (h *Hook) OnToolStart(ctx context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	ok, err := h.approver(ctx, call)
	if err != nil {
		return hook.ActionProceed, err
	}
	if ok {
		return hook.ActionProceed, nil
	}
	return h.onDeny, nil
}
