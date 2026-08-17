// Package task 提供 task 工具：主 Agent 把可隔离的子任务委派给一个独立
// 子 Agent（核心是 core.NewAgent + Run 的完整 model↔tool 循环）执行，
// 子循环结束后把最终答案作为工具结果返回主循环。
//
// 与 approve/askuser/taskplan 同构：OnToolStart 拦截 task 调用，
// 但等待的不是人，而是子 Agent 的完整运行。与它们不同，task 工具由 hook 在
// OnStart 自动注册（同 mcp/filetools），core.WithHooks(New(...)) 即可，无需
// 再 core.WithTools(Tool())。子 Agent 默认继承主 Agent 当前 state 的全部工具
// （剔除 task 自身，防止无界递归），因此是"基于当前 state 派生子循环"；
// 也可用 WithTools 注入专属工具、WithInheritTools(false) 完全隔离。
package task

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// ToolName 是子任务委派工具的注册名。
const ToolName = "task"

// EventStart 是子 Agent 启动事件，Data 为 *types.ToolCall。
const EventStart = event.EventType("task.start")

// EventEnd 是子 Agent 结束事件，Data 为 *types.LoopState（子循环最终状态）。
const EventEnd = event.EventType("task.end")

// Options 配置子 Agent 的组装方式。
type Options struct {
	// SystemPrompt 是子 Agent 的系统提示词（人格、约束等），独立于主 Agent。
	SystemPrompt string
	// MaxIterations 子 Agent 最大迭代次数，0 用框架默认。
	MaxIterations int
	// Tools 是额外注入子 Agent 的工具；InheritTools 为 false 时作为唯一工具集。
	Tools []types.Tool
	// InheritTools 为 true（默认）时，子 Agent 继承主 Agent 当前 state 的全部
	// 工具（task 自身被剔除，防止无界递归）。设为 false 则仅用 Tools。
	InheritTools bool
	// Streaming 子 Agent 是否流式（chunk 经 OnEvent 转发）。
	Streaming bool
	// OnEvent 子 Agent 自身事件出口（观察子循环，可为 nil）。
	OnEvent event.OnEvent
}

// Option 配置子 Agent 组装方式。
type Option func(*Options)

// WithSystemPrompt 设置子 Agent 系统提示词。
func WithSystemPrompt(s string) Option { return func(o *Options) { o.SystemPrompt = s } }

// WithMaxIterations 设置子 Agent 最大迭代次数。
func WithMaxIterations(n int) Option { return func(o *Options) { o.MaxIterations = n } }

// WithTools 注入子 Agent 专属工具（额外工具，或 InheritTools=false 时的唯一工具集）。
func WithTools(t ...types.Tool) Option { return func(o *Options) { o.Tools = append(o.Tools, t...) } }

// WithInheritTools 控制是否继承主 Agent 当前 state 的工具（默认 true）。
func WithInheritTools(b bool) Option { return func(o *Options) { o.InheritTools = b } }

// WithStreaming 控制子 Agent 是否流式。
func WithStreaming(b bool) Option { return func(o *Options) { o.Streaming = b } }

// WithOnEvent 设置子 Agent 自身事件出口。
func WithOnEvent(fn event.OnEvent) Option { return func(o *Options) { o.OnEvent = fn } }

// Hook 拦截 task 工具调用并运行子 Agent。
type Hook struct {
	provider provider.ModelProvider
	opts     Options
	mu       sync.Mutex // 保护 state.Usage 累加（同一轮多个 task 调用并发）
}

// New 创建 task hook。p 是子 Agent 的模型（可与主 Agent 相同，或用更便宜/专用模型）。
// task 工具由 hook 在 OnStart 自动注册，core.WithHooks(New(...)) 即可，
// 无需再 core.WithTools(Tool())。
func New(p provider.ModelProvider, opts ...Option) *Hook {
	if p == nil {
		panic("task: nil sub-agent provider")
	}
	o := Options{InheritTools: true}
	for _, fn := range opts {
		fn(&o)
	}
	return &Hook{provider: p, opts: o}
}

func (h *Hook) Name() string { return "task" }

// OnStart 自动注册 task 工具：主模型无需 core.WithTools(Tool()) 即可发现它。
// 仍导出 Tool() 以便显式装配（如注入子 Agent 支持嵌套委派）。
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(Tool())
	return nil
}

// OnToolStart 拦截 task 调用：基于当前 state 派生子 Agent，跑完子循环后
// 把最终答案作为工具结果回传主循环（Skip），主循环继续。
func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name != ToolName {
		return hook.Proceed, nil
	}
	var args struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal(call.Args, &args)
	if args.Task == "" {
		return hook.Skip("task: empty subtask description"), nil
	}

	state.EmitEvent(EventStart, call)
	sub, err := h.runSub(ctx, state, args.Task)
	state.EmitEvent(EventEnd, sub)

	if err != nil {
		if ctx.Err() != nil {
			return hook.Skip(""), ctx.Err()
		}
		// 子循环出错不终止主循环：与"工具错误回传模型自纠"同语义，
		// 错误作为工具结果进入历史，主模型据此重试或调整。
		return hook.Skip("task failed: " + err.Error()), nil
	}
	h.accumulateUsage(state, sub.Usage)
	return hook.Skip(formatResult(sub)), nil
}

// runSub 基于父 state 组装子 Agent 并跑完一次完整循环。
func (h *Hook) runSub(ctx context.Context, state *types.LoopState, task string) (*types.LoopState, error) {
	opts := []core.Option{}
	if h.opts.SystemPrompt != "" {
		opts = append(opts, core.WithSystemPrompt(h.opts.SystemPrompt))
	}
	if h.opts.MaxIterations > 0 {
		opts = append(opts, core.WithMaxIterations(h.opts.MaxIterations))
	}
	if h.opts.Streaming {
		opts = append(opts, core.WithStreaming(true))
	}
	if h.opts.InheritTools {
		opts = append(opts, core.WithTools(inheritedTools(state)...))
	}
	if len(h.opts.Tools) > 0 {
		opts = append(opts, core.WithTools(h.opts.Tools...))
	}
	if h.opts.OnEvent != nil {
		opts = append(opts, core.WithOnEvent(h.opts.OnEvent))
	}
	return core.NewAgent(h.provider, opts...).Run(ctx, task)
}

// inheritedTools 从父 state 派生子 Agent 工具集：剔除 task 自身，
// 避免子 Agent 再次委派造成无界递归；其余原样继承（含父 Agent 的 warp 链）。
func inheritedTools(state *types.LoopState) []types.Tool {
	list := state.Tools.List()
	out := make([]types.Tool, 0, len(list))
	for _, t := range list {
		if t.Name() == ToolName {
			continue
		}
		out = append(out, t)
	}
	return out
}

// accumulateUsage 把子循环用量累加到父 state。多个 task 调用并发执行，
// 经 h.mu 串行化；引擎在工具执行段不写 state.Usage，故锁内累加安全。
func (h *Hook) accumulateUsage(state *types.LoopState, u types.Usage) {
	h.mu.Lock()
	state.Usage.Add(u)
	h.mu.Unlock()
}

// formatResult 取子循环的最终回答作为工具结果；非正常结束附上说明，
// 供主模型判断子任务是否真正完成。
func formatResult(sub *types.LoopState) string {
	ans := lastAssistantContent(sub)
	if ans == "" {
		ans = "(subtask completed with no output)"
	}
	switch sub.StopReason {
	case types.StopMaxIteration:
		return ans + "\n\n[subtask reached max iterations, result may be incomplete]"
	case types.StopAborted, types.StopCancelled:
		return ans + "\n\n[subtask stopped: " + string(sub.StopReason) + "]"
	default:
		return ans
	}
}

func lastAssistantContent(sub *types.LoopState) string {
	for i := len(sub.Messages) - 1; i >= 0; i-- {
		if sub.Messages[i].Role == types.RoleAssistant {
			return sub.Messages[i].Content
		}
	}
	return ""
}

// Tool 返回 task 壳工具：仅提供 schema 供主模型发现，
// 真正的"执行体"是 Hook 拦截后运行子 Agent，Invoke 不会被走到。
func Tool() types.Tool { return tool{} }

type tool struct{}

func (tool) Name() string { return ToolName }
func (tool) Description() string {
	return "把子任务委派给独立子代理执行：子代理用自己的模型-工具循环完成任务并返回最终结果。适合可隔离、需多步工具调用或耗时较长的子任务。"
}
func (tool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":"子任务的完整描述，包含目标与验收标准"}},"required":["task"]}`)
}
func (tool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	// 防呆：未挂 Hook 时尽早暴露装配错误。
	return "", errors.New("task: hook not registered (task is intercepted by task.Hook)")
}
