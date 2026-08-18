// Package summary 在 loop 结束时调用模型对整个过程生成摘要，
// 结果写入 state.Metadata["summary"]（失败不阻断主流程）。
//
// 摘要是一次额外的全量模型调用——不要无脑每轮都做：MinMessages 设阈值
// 让短会话跳过；或不挂 hook，直接调 Summarize 按需触发（如手动命令）。
package summary

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

const DefaultPrompt = "用一段话总结以下对话：用户的目标是什么、调用了哪些工具、最终结果如何。"

// SummaryTimeout 限制摘要调用时长：EndHook 收到的是脱离取消的 ctx，
// 无限等待会拖住 Run / RunAsync.Wait 永不返回。
const SummaryTimeout = 30 * time.Second

// Options 配置摘要行为。
type Options struct {
	// Prompt 摘要指令，默认 DefaultPrompt。
	Prompt string
	// MinMessages 历史消息数达到该值才摘要——摘要要为全量历史付一次
	// 模型调用，短会话跳过；0 = 每轮都摘。
	MinMessages int
}

// Option 配置摘要行为。
type Option func(*Options)

// WithPrompt 自定义摘要指令。
func WithPrompt(p string) Option { return func(o *Options) { o.Prompt = p } }

// WithMinMessages 设历史消息阈值，达到才摘要（0 = 每轮都摘）。
func WithMinMessages(n int) Option { return func(o *Options) { o.MinMessages = n } }

type Hook struct {
	p    provider.ModelProvider
	opts Options
}

func New(p provider.ModelProvider, opts ...Option) *Hook {
	o := Options{}
	for _, fn := range opts {
		fn(&o)
	}
	return &Hook{p: p, opts: o}
}

func (h *Hook) Name() string { return "summary" }

func (h *Hook) OnEnd(ctx context.Context, state *types.LoopState) error {
	if len(state.Messages) < h.opts.MinMessages {
		return nil
	}
	s, err := Summarize(ctx, h.p, state.Messages, h.opts.Prompt)
	if err != nil {
		// 摘要失败不影响主流程结果。
		state.Metadata["summary_error"] = err.Error()
		return nil
	}
	state.Metadata["summary"] = s
	return nil
}

// Summarize 调用模型总结一段消息历史（hook 内部使用，也可按需手动调用）。
func Summarize(ctx context.Context, p provider.ModelProvider, msgs []types.Message, prompt string) (string, error) {
	if prompt == "" {
		prompt = DefaultPrompt
	}
	ctx, cancel := context.WithTimeout(ctx, SummaryTimeout)
	defer cancel()
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%s: %s", m.Role, m.Content)
		if m.Err != "" {
			fmt.Fprintf(&b, " (error: %s)", m.Err)
		}
		b.WriteByte('\n')
	}
	resp, err := p.Invoke(ctx, &types.ModelRequest{Messages: []types.Message{
		{Role: types.RoleUser, Content: prompt + "\n\n" + b.String()},
	}})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
