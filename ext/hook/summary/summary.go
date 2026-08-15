// Package summary 在 loop 结束时调用模型对整个过程生成摘要，
// 结果写入 state.Metadata["summary"]（失败不阻断主流程）。
package summary

import (
	"context"
	"fmt"
	"strings"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

const DefaultPrompt = "用一段话总结以下对话：用户的目标是什么、调用了哪些工具、最终结果如何。"

type Hook struct {
	p      provider.ModelProvider
	prompt string
}

func New(p provider.ModelProvider, prompt string) *Hook {
	if prompt == "" {
		prompt = DefaultPrompt
	}
	return &Hook{p: p, prompt: prompt}
}

func (h *Hook) Name() string { return "summary" }

func (h *Hook) OnEnd(ctx context.Context, state *types.LoopState) error {
	var b strings.Builder
	for _, m := range state.Messages {
		fmt.Fprintf(&b, "%s: %s", m.Role, m.Content)
		if m.Err != "" {
			fmt.Fprintf(&b, " (error: %s)", m.Err)
		}
		b.WriteByte('\n')
	}
	resp, err := h.p.Invoke(ctx, &types.ModelRequest{Messages: []types.Message{
		{Role: types.RoleUser, Content: h.prompt + "\n\n" + b.String()},
	}})
	if err != nil {
		// 摘要失败不影响主流程结果。
		state.Metadata["summary_error"] = err.Error()
		return nil
	}
	state.Metadata["summary"] = resp.Content
	return nil
}
