package approve

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/hook"
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

// 拒绝 → Skip（loop 继续）；Store 批准后放行（轮次式审批核心）。
func TestApproveDenyAndStore(t *testing.T) {
	store := NewStore(true)
	denied := New(store.Approver())

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
	if !strings.Contains(state.Messages[2].Content, "skipped by tool-start hook: echo") {
		t.Fatalf("denied: %q", state.Messages[2].Content)
	}

	// 用户批准（args 指纹命中）后放行；approve-once 只放行一次。
	store.Approve("echo", json.RawMessage(`{"a":1}`))
	if !store.IsApproved("echo", json.RawMessage(`{"a":1}`)) {
		t.Fatal("approved call must pass")
	}
	if store.IsApproved("echo", json.RawMessage(`{"a":1}`)) {
		t.Fatal("approve-once store must consume on hit")
	}
	if store.IsApproved("echo", json.RawMessage(`{"a":2}`)) {
		t.Fatal("changed args must not hit")
	}
}

func TestAbortMode(t *testing.T) {
	state, _ := core.NewAgent(
		testutil.Scripted(testutil.ToolCalls(testutil.Call("1", "echo", `{}`))),
		core.WithTools(echoTool{}),
		core.WithHooks(New(func(context.Context, *types.ToolCall) (bool, error) {
			return false, nil
		}, hook.ActionAbort)),
	).Run(context.Background(), "hi")
	if state.StopReason != types.StopAborted {
		t.Fatalf("stop: %s", state.StopReason)
	}
}
