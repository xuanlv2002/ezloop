package approve

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

type echoTool struct{}

func (echoTool) Name() string                { return "echo" }
func (echoTool) Description() string         { return "" }
func (echoTool) ArgsSchema() json.RawMessage { return nil }
func (echoTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	return "ran " + string(args), nil
}

// 拒绝（带理由）→ 理由作为工具结果进入历史，loop 继续；批准 → 工具真执行。
func TestApproveDecisions(t *testing.T) {
	denied, denyCh := New(nil)
	go func() { denyCh <- Decision{CallID: "1", Reason: "readonly env"} }()

	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "echo", `{"a":1}`)),
			testutil.Text("done"),
		),
		core.WithTools(echoTool{}),
		core.WithHooks(denied),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("deny must continue loop: %s", state.StopReason)
	}
	if !strings.Contains(state.Messages[2].Content, "denied by user: readonly env") {
		t.Fatalf("denied msg: %q", state.Messages[2].Content)
	}

	approved, okCh := New(nil)
	go func() { okCh <- Decision{Approve: true} }()

	state2, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "echo", `{"a":1}`)),
			testutil.Text("done"),
		),
		core.WithTools(echoTool{}),
		core.WithHooks(approved),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state2.Messages[2].Content != `ran {"a":1}` {
		t.Fatalf("approved msg: %q", state2.Messages[2].Content)
	}
}

// needs 过滤：返回 false 的调用不经审批直接放行。
func TestApproveNeedsFilter(t *testing.T) {
	h, ch := New(func(c *types.ToolCall) bool { return c.Name != "safe" })
	go func() {
		ch <- Decision{CallID: "2"} // 只有 call 2 走审批
	}()

	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("1", "safe", `{}`), // 免审直接执行
				testutil.Call("2", "echo", `{}`), // 审批拒绝（无理由→默认文案）
			),
			testutil.Text("done"),
		),
		core.WithTools(testutil.EchoTool{}, safeTool{}),
		core.WithHooks(h),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Messages[2].Content != "ran" {
		t.Fatalf("safe msg: %q", state.Messages[2].Content)
	}
	if !strings.Contains(state.Messages[3].Content, "denied by user") {
		t.Fatalf("denied msg: %q", state.Messages[3].Content)
	}
}

type safeTool struct{}

func (safeTool) Name() string                { return "safe" }
func (safeTool) Description() string         { return "" }
func (safeTool) ArgsSchema() json.RawMessage { return nil }
func (safeTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return "ran", nil
}

// 无人决策 + ctx 取消 → StopCancelled，消息历史仍完整。
func TestApproveCancel(t *testing.T) {
	h, _ := New(nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	state, err := core.NewAgent(
		testutil.Scripted(testutil.ToolCalls(testutil.Call("1", "echo", `{}`))),
		core.WithTools(echoTool{}),
		core.WithHooks(h),
	).Run(ctx, "hi")
	if err == nil || state.StopReason != types.StopCancelled {
		t.Fatalf("stop=%s err=%v", state.StopReason, err)
	}
	if len(state.Messages) != 3 || state.Messages[2].ToolCallID != "1" {
		t.Fatalf("history incomplete: %d messages", len(state.Messages))
	}
}

// 判定段并发：多个需审批调用同时请求、决策并发回传，全部批准后全部执行且结果保序。
func TestConcurrentApprovals(t *testing.T) {
	h, ch := New(nil)
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("1", "echo", `{"n":1}`),
				testutil.Call("2", "echo", `{"n":2}`),
				testutil.Call("3", "echo", `{"n":3}`),
				testutil.Call("4", "echo", `{"n":4}`),
				testutil.Call("5", "echo", `{"n":5}`),
			),
			testutil.Text("done"),
		),
		core.WithTools(echoTool{}),
		core.WithHooks(h),
		core.WithOnEvent(func(e event.Event) {
			if e.Type != EventRequest {
				return
			}
			call := e.Data.(*types.ToolCall)
			go func() { ch <- Decision{CallID: call.ID, Approve: true} }()
		}),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	for i := 0; i < 5; i++ {
		want := fmt.Sprintf(`ran {"n":%d}`, i+1)
		if state.Messages[2+i].Content != want {
			t.Fatalf("msg[%d]=%q want %q", i, state.Messages[2+i].Content, want)
		}
	}
}
