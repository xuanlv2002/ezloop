// Package askuser 提供 ask_user 工具：模型向用户提问，循环中断等待回答，
// 回答作为工具结果进入消息历史。
// 实现与 approve 同构：OnToolStart 阻塞在 Answers channel 上，
// 中断/恢复对引擎透明。ask_user 工具由 hook 在 OnStart 自动注册，
// core.WithHooks(New()) 即可，无需再 core.WithTools(Tool())。
package askuser

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/hook/internal/await"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

// ToolName 是提问工具的注册名。
const ToolName = "ask_user"

// EventRequest 是提问事件，Data 为 *types.ToolCall（Args.question 为问题）。
const EventRequest = event.EventType("askuser.request")

// Answer 是对一次提问的回应。CallID 留空视为回应当前请求。
type Answer struct {
	CallID string
	Input  string // 用户回答，原样进入消息历史
}

type Hook struct {
	router *await.Router[Answer]
}

// New 创建 hook 并返回回答 channel 的发送端。
// ask_user 工具由 hook 在 OnStart 自动注册，无需再 core.WithTools(Tool())。
// 回答必须从其他 goroutine 发送，理由同 approve.New。
func New() (*Hook, chan<- Answer) {
	ch := make(chan Answer)
	return &Hook{router: await.New(ch, func(a Answer) string { return a.CallID })}, ch
}

func (h *Hook) Name() string { return "askuser" }

// OnStart 自动注册 ask_user 工具。仍导出 Tool() 以便显式装配。
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(Tool())
	return nil
}

func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name != ToolName {
		return hook.Proceed, nil
	}
	state.EmitEvent(EventRequest, call)
	a, ok := h.router.Await(ctx, call.ID)
	if !ok {
		return hook.Skip(""), ctx.Err()
	}
	return answerAction(a), nil
}

func answerAction(a Answer) hook.Action {
	if a.Input == "" {
		a.Input = "(user gave no input)"
	}
	return hook.Skip(a.Input)
}

// Tool 返回 ask_user 壳工具：仅提供 schema 供模型发现，
// 真正的"执行体"是 Hook 拦截后等用户回答，Invoke 不会被走到。
// hook 已在 OnStart 自动注册本工具，通常无需手动调用。
func Tool() types.Tool { return tool{} }

type tool struct{}

func (tool) Name() string { return ToolName }
func (tool) Description() string {
	return "向用户提问并等待回答。缺少必要信息、需要澄清或确认方向时使用，不要替用户假设。"
}
func (tool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"question":{"type":"string","description":"要问用户的问题"}},"required":["question"]}`)
}
func (tool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	// 防呆：未挂 Hook 时尽早暴露装配错误。
	return "", errors.New("askuser: hook not registered (ask_user is intercepted by askuser.Hook)")
}
