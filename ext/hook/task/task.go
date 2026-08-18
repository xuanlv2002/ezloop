// Package task 提供 task 工具：一个"并行分身"工具（fork）。
//
// 主模型调用 task 干某个可并行的活时，不是注入一个独立 subAgent，而是
// fork 当前 Agent（同一 provider / model warp / 超参 / 全部运行期 hook）
// 与当前上下文（截至本轮之前的消息快照），在隔离的消息历史上再跑一轮
// model↔tool 循环：子循环内部的过程性上下文（中间的工具调用、思考步骤）
// 不回流主上下文，只有最终答案作为工具结果回传主循环。
//
// 特性：
//   - 并发：工具本就并发，多个 task 调用在同一轮内并行 fork；
//   - 一致：分身继承全部运行期 hook——审批照拦、可问用户（approve/askuser
//     的事件带 ForkID，渲染层据此区分是哪个分身在问）、session 照存
//     （localsession 按 ForkID 分流到独立文件，可回放）；
//   - 单层：fork 的工具集剔除 task 自身，且 Agent.Fork 不重跑 startHooks、
//     子循环内不再注册 task，fork 不能再 fork；
//   - 标识：子循环所有事件（引擎事件与 hook 事件）由引擎统一打上 ForkID。
//
// 与 approve/askuser/taskplan 同构：OnToolStart 拦截 task 调用，等待的不是人，
// 而是 fork 子循环的完整运行。task 工具由 hook 在 OnStart 自动注册
// （同 mcp/filetools），core.WithHooks(New()) 即可，无需 core.WithTools(Tool())。
package task

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

// ToolName 是上下文隔离工具的注册名。
const ToolName = "task"

// EventStart 是 fork 启动事件，Data 为 *types.ToolCall。
const EventStart = event.EventType("task.start")

// EventEnd 是 fork 结束事件，Data 为 *types.LoopState（子循环最终状态）。
const EventEnd = event.EventType("task.end")

// taskInputPrefix 包装任务描述。拼在 input（请求最末尾）而非 system：
// 不动 seed 前缀，主循环 KV 缓存对分身依旧可复用。回传机制只保证取
// 最后一条 assistant 消息，不保证其质量——这里引导分身以自包含的最终
// 结果收尾，主循环拿到的工具结果才有内容。
const taskInputPrefix = "以下是你独立负责的子任务，请完成它；你的最后一条回复将作为结果直接交付，须自包含、简洁：\n\n"

// Options 配置 fork 的工具集。
type Options struct {
	// Tools 是额外注入 fork 的工具；InheritTools 为 false 时作为唯一工具集。
	Tools []types.Tool
	// InheritTools 为 true（默认）时，fork 继承主 Agent 当前 state 的全部
	// 工具（task 自身被剔除，保证单层）。设为 false 则仅用 Tools。
	InheritTools bool
}

// Option 配置 fork 的工具集。
type Option func(*Options)

// WithTools 注入 fork 专属工具（额外工具，或 InheritTools=false 时的唯一工具集）。
// 注意：注入的工具不经过主循环 tool warp 链（继承的工具已带 warp 壳，为避免
// 双重包装 fork 不再包装），需要防护/审计请自行包装后再传入。
func WithTools(t ...types.Tool) Option { return func(o *Options) { o.Tools = append(o.Tools, t...) } }

// WithInheritTools 控制是否继承主 Agent 当前 state 的工具（默认 true）。
func WithInheritTools(b bool) Option { return func(o *Options) { o.InheritTools = b } }

// Hook 拦截 task 工具调用，复刻当前 Agent 与上下文跑一次隔离子循环。
type Hook struct {
	opts Options
	mu   sync.Mutex    // 保护 state.Usage 累加（同一轮多个 task 调用并发）
	seq  atomic.Uint64 // forkID 序号
}

// New 创建 task hook。fork 复刻的主 Agent 从 ctx 取（引擎每次 Run 注入，
// 见 core.AgentFromContext），无需组装期绑定：core.WithHooks(New()) 一步到位。
func New(opts ...Option) *Hook {
	o := Options{InheritTools: true}
	for _, fn := range opts {
		fn(&o)
	}
	return &Hook{opts: o}
}

func (h *Hook) Name() string { return "task" }

// OnStart 自动注册 task 工具：主模型无需 core.WithTools(Tool()) 即可发现它。
// 仍导出 Tool() 以便显式装配。
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(Tool())
	return nil
}

// OnToolStart 拦截 task 调用：复刻当前上下文与工具集，跑完隔离子循环后把
// 最终答案作为工具结果回传主循环（Skip），主循环继续。
func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name != ToolName {
		return hook.Proceed, nil
	}
	// 单层：分身继承本 hook，子循环内若模型再调 task（工具集已剔除，
	// 这里防幻觉调用）直接拒绝，不再嵌套 fork。
	if state.ForkID != "" {
		return hook.Skip("task: nested fork is not allowed (single level)"), nil
	}
	var args struct {
		Task string `json:"task"`
	}
	_ = json.Unmarshal(call.Args, &args)
	if args.Task == "" {
		return hook.Skip("task: empty task description"), nil
	}
	agent, ok := core.AgentFromContext(ctx)
	if !ok {
		// 防呆：不在引擎 Run 内运行（裸调 hook 的测试等场景）。
		return hook.Skip("task: no agent in ctx (must run via core.Agent.Run)"), nil
	}

	forkID := h.newForkID()

	// seed：截至本轮之前的主上下文（剔除本轮 assistant 工具调用消息），
	// fork 从同一上下文继续，追加任务描述作为用户输入。
	seed := contextSnapshot(state)
	// tools：继承当前 state 工具，剔除 task 自身（单层保证）。
	tools := h.forkTools(state)

	h.emit(state, EventStart, call, forkID)
	sub, err := agent.Fork(ctx, forkID, seed, tools, taskInputPrefix+args.Task)
	h.emit(state, EventEnd, sub, forkID)

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

// newForkID 生成任务标识。并发安全（同一轮多个 task 调用并发 fork）。
func (h *Hook) newForkID() string {
	return "task-" + strconv.FormatUint(h.seq.Add(1), 10)
}

// contextSnapshot 返回"当前上下文"快照：截至本轮 assistant 工具调用消息
// 之前的全部消息，并剔除触发本次 task 的那条 user 指令——它面向主我，
// 任务描述以 task 参数为准（schema 要求自包含），留在 seed 里只会让
// 分身把主我接到的指令也当作要回应的问题。触发指令必在倒数第二位
// （OnToolStart 时末尾是携带 tool_calls 的 assistant 消息）；若不是
// user（多轮工具循环后直接调 task 的场景）则不动。
func contextSnapshot(state *types.LoopState) []types.Message {
	if len(state.Messages) == 0 {
		return nil
	}
	msgs := state.Messages[:len(state.Messages)-1]
	if len(msgs) > 0 && msgs[len(msgs)-1].Role == types.RoleUser {
		msgs = msgs[:len(msgs)-1]
	}
	return msgs
}

// forkTools 派生 fork 的工具集：继承当前 state 工具并剔除 task 自身
// （单层保证），再叠加 Options.Tools；InheritTools=false 时仅用 Options.Tools。
func (h *Hook) forkTools(state *types.LoopState) []types.Tool {
	var tools []types.Tool
	if h.opts.InheritTools {
		tools = inheritedTools(state)
	}
	return append(tools, h.opts.Tools...)
}

// inheritedTools 从父 state 派生 fork 工具集：剔除 task 自身，避免 fork
// 再次隔离造成递归；其余原样继承（含父 Agent 的 warp 链）。
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

// emit 发送带 forkID 标识的事件（task.start / task.end）。
func (h *Hook) emit(state *types.LoopState, typ event.EventType, data any, forkID string) {
	if state.Emitter == nil {
		return
	}
	state.Emitter(event.Event{
		Type:      typ,
		Timestamp: time.Now(),
		Iteration: state.Iteration,
		ForkID:    forkID,
		Data:      data,
	})
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
// 真正的"执行体"是 Hook 拦截后跑隔离子循环，Invoke 不会被走到。
func Tool() types.Tool { return tool{} }

type tool struct{}

func (tool) Name() string { return ToolName }
func (tool) Description() string {
	return "在隔离上下文中求解子任务：复刻当前模型与上下文独立运行，只把最终结果带回主对话。适合需要多步工具调用、过程细节无需回流的子问题。"
}
func (tool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"task":{"type":"string","description":"子任务的完整描述，包含目标与验收标准"}},"required":["task"]}`)
}
func (tool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	// 防呆：未挂 Hook 时尽早暴露装配错误。
	return "", errors.New("task: hook not registered (task is intercepted by task.Hook)")
}
