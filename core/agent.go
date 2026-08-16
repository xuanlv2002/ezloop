// Package core 实现 loop 引擎：NewAgent 组装 Provider、Hook 与工具，
// Run 驱动 "model → tool → model" 循环直到完成或终止。
package core

import (
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

const DefaultMaxIterations = 16

// HyperParams 是 Agent 的运行超参数。
type HyperParams struct {
	// MaxIterations 最大迭代次数，0 取默认 16。
	MaxIterations int
	// MaxConcurrency 单轮工具并发执行上限，1 为串行（默认）。
	MaxConcurrency int
}

func (hp HyperParams) withDefaults() HyperParams {
	if hp.MaxIterations <= 0 {
		hp.MaxIterations = DefaultMaxIterations
	}
	if hp.MaxConcurrency <= 0 {
		hp.MaxConcurrency = 1
	}
	return hp
}

type Agent struct {
	provider  provider.ModelProvider
	streaming bool

	startHooks      []hook.StartHook
	modelStartHooks []hook.ModelStartHook
	modelEndHooks   []hook.ModelEndHook
	toolStartHooks  []hook.ToolStartHook
	toolEndHooks    []hook.ToolEndHook
	loopHooks       []hook.LoopHook
	endHooks        []hook.EndHook

	tools      []types.Tool
	toolWarps  []types.ToolWarpHandler
	hyper      HyperParams
	onEvent    event.OnEvent
	systemPrompt string
}

type Option func(*Agent)

// WithHooks 传入任意扩展（mcp、skill、异常捕获等），
// 每个扩展只需实现 hook 包中它关心的小接口。
func WithHooks(hooks ...hook.Hook) Option {
	return func(a *Agent) {
		for _, h := range hooks {
			if h == nil {
				continue
			}
			if v, ok := h.(hook.StartHook); ok {
				a.startHooks = append(a.startHooks, v)
			}
			if v, ok := h.(hook.ModelStartHook); ok {
				a.modelStartHooks = append(a.modelStartHooks, v)
			}
			if v, ok := h.(hook.ModelEndHook); ok {
				a.modelEndHooks = append(a.modelEndHooks, v)
			}
			if v, ok := h.(hook.ToolStartHook); ok {
				a.toolStartHooks = append(a.toolStartHooks, v)
			}
			if v, ok := h.(hook.ToolEndHook); ok {
				a.toolEndHooks = append(a.toolEndHooks, v)
			}
			if v, ok := h.(hook.LoopHook); ok {
				a.loopHooks = append(a.loopHooks, v)
			}
			if v, ok := h.(hook.EndHook); ok {
				a.endHooks = append(a.endHooks, v)
			}
		}
	}
}

func WithTools(tools ...types.Tool) Option {
	return func(a *Agent) { a.tools = append(a.tools, tools...) }
}

func WithMaxIterations(n int) Option {
	return func(a *Agent) { a.hyper.MaxIterations = n }
}

// WithHyperParams 设置运行超参数（迭代上限、工具并发量等）。
func WithHyperParams(hp HyperParams) Option {
	return func(a *Agent) { a.hyper = hp }
}

func WithOnEvent(fn event.OnEvent) Option {
	return func(a *Agent) { a.onEvent = fn }
}

// WithSystemPrompt 设置 agent 级系统提示词（人格、规则等），
// 位于消息序列最前。会话级动态注入用 skill hook 或 WithHistory。
func WithSystemPrompt(prompt string) Option {
	return func(a *Agent) { a.systemPrompt = prompt }
}

// RunOption 是单次 Run 的定制项。
type RunOption func(*types.LoopState)

// WithHistory 携带历史对话消息（多轮会话延续），
// 历史位于本次 input 之前、system 提示词之后。
// system 消息会被过滤：它们是 agent 的属性（WithSystemPrompt、skill hook），
// 每轮由引擎重新注入，历史快照中的 system 不应重复带入。
func WithHistory(messages ...types.Message) RunOption {
	return func(state *types.LoopState) {
		for _, m := range messages {
			if m.Role == types.RoleSystem {
				continue
			}
			state.Messages = append(state.Messages, m)
		}
	}
}

// WithStreaming 启用后，Provider 若实现 StreamProvider 则走流式，
// chunk 通过 EventModelChunk 实时发出。
func WithStreaming(enabled bool) Option {
	return func(a *Agent) { a.streaming = enabled }
}

// WithModelWarp 传入模型节点中间件（引擎标准能力），
// 在 NewAgent 时包装 provider。先注册的位于最外层。
func WithModelWarp(warps ...provider.WarpHandler) Option {
	return func(a *Agent) {
		a.provider = provider.Warp(a.provider, warps...)
	}
}

// WithToolWarp 传入工具节点中间件（引擎标准能力）。
// 挂载在 ToolRegistry 上：静态注册（WithTools）与 hook 运行时注入的工具
// 都会被包装。先注册的位于最外层。
func WithToolWarp(warps ...types.ToolWarpHandler) Option {
	return func(a *Agent) {
		a.toolWarps = append(a.toolWarps, warps...)
	}
}

func NewAgent(p provider.ModelProvider, opts ...Option) *Agent {
	a := &Agent{
		provider: p,
		onEvent:  event.Noop,
	}
	for _, opt := range opts {
		opt(a)
	}
	a.hyper = a.hyper.withDefaults()
	return a
}
