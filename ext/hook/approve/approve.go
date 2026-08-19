/*
Package approve 提供工具调用审批：工具执行前发审批事件并阻塞等待
使用方经 Decisions channel 送回的决策。
中断 = goroutine 阻塞在 channel 上（进程内天然暂停/恢复，引擎无感知）；
跨进程恢复不依赖本包——消息历史永远完整，恢复即新对话。
*/
package approve

import (
	"context"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/hook/internal/await"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

/*
EventRequest 是审批请求事件，Data 为 *types.ToolCall。
使用方将其呈现给用户（CLI 打印、推送前端），再经 channel 送回决策。
*/
const EventRequest = event.EventType("approve.request")

/*
Decision 是对一次审批请求的回应。CallID 留空视为回应当前请求，
不匹配的过期决策被忽略。
*/
type Decision struct {
	CallID  string
	Approve bool
	Reason  string // 拒绝理由，将作为工具结果进入消息历史供模型参考
}

type Hook struct {
	router *await.Router[Decision]
	needs  func(*types.ToolCall) bool
}

/*
New 创建审批 hook，并返回决策 channel 的发送端。
needs 为 nil 时全部工具需审批；返回 false 的调用直接放行。
needs 拿到完整 ToolCall（含 Args），支持参数值级判断——
如 bash 只放行白名单命令、write_file 只审计特定路径外写操作：

	approve.New(func(c *types.ToolCall) bool {
	    if c.Name == "terminal" {
	        var a struct{ Command string `json:"command"` }
	        _ = json.Unmarshal(c.Args, &a)
	        return !isReadOnlyCommand(a.Command) // 按命令内容决定是否中断
	    }
	    return true
	})

决策必须从其他 goroutine 发送（如事件回调中 go 发送、WebSocket handler），
同步在 OnEvent 回调里发送会与 hook 的等待互相死锁。
*/
func New(needs func(*types.ToolCall) bool) (*Hook, chan<- Decision) {
	ch := make(chan Decision)
	return &Hook{
		router: await.New(ch, func(d Decision) string { return d.CallID }),
		needs:  needs,
	}, ch
}

func (h *Hook) Name() string { return "approve" }

func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if h.needs != nil && !h.needs(call) {
		return hook.Proceed, nil
	}
	state.EmitEvent(EventRequest, call)
	d, ok := h.router.Await(ctx, call.ID)
	if !ok {
		return hook.Skip(""), ctx.Err()
	}
	return h.decide(d), nil
}

func (h *Hook) decide(d Decision) hook.Action {
	if d.Approve {
		return hook.Proceed
	}
	reason := "denied by user"
	if d.Reason != "" {
		reason += ": " + d.Reason
	}
	return hook.Skip(reason)
}
