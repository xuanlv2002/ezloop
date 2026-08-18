package localsession

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// 完整链路：loop 结束自动持久化 → Load 恢复 → WithHistory 续聊再覆盖。
func TestSessionRoundtrip(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys, "chat-1")

	state1, err := core.NewAgent(
		testutil.Scripted(testutil.Text("hello")),
		core.WithHooks(hook),
	).Run(context.Background(), "第一轮")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if state1.Metadata["localsession_error"] != nil {
		t.Fatalf("persist error: %v", state1.Metadata["localsession_error"])
	}

	s, err := Load(context.Background(), fsys, "", "chat-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if s.ID != "chat-1" || len(s.Messages) != 2 || s.Messages[1].Content != "hello" {
		t.Fatalf("session: %+v", s)
	}

	// 恢复续聊：同一 ID 滚动覆盖。
	state2, err := core.NewAgent(
		testutil.Scripted(testutil.Text("world")),
		core.WithHooks(hook),
	).Run(context.Background(), "第二轮", core.WithHistory(s.Messages...))
	if err != nil {
		t.Fatalf("run2: %v", err)
	}
	s2, err := Load(context.Background(), fsys, "", "chat-1")
	if err != nil {
		t.Fatalf("load2: %v", err)
	}
	// user1+asst1+user2+asst2
	if len(s2.Messages) != 4 || s2.Messages[3].Content != "world" {
		t.Fatalf("session2: %d msgs", len(s2.Messages))
	}
	_ = state2

	ids, err := List(context.Background(), fsys, "")
	if err != nil || len(ids) != 1 || ids[0] != "chat-1" {
		t.Fatalf("list: %v err=%v", ids, err)
	}
}

// fork 分流：子循环（TaskID 非空）写独立 <主ID>-<taskID>.json，
// 与主会话互不覆盖，Load 可回放分身的完整过程。
func TestForkSessionSplit(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	ls := New(fsys, "chat-1")

	// 分身：同一 Agent fork 出去，endHook 继承 → 子 session 落独立文件。
	sub, err := core.NewAgent(
		testutil.Scripted(testutil.Text("sub out")),
		core.WithHooks(ls),
	).Fork(context.Background(), "task-3", nil, nil, "subtask")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if sub.Metadata["localsession_error"] != nil {
		t.Fatalf("persist error: %v", sub.Metadata["localsession_error"])
	}
	s, err := Load(context.Background(), fsys, "", "chat-1-task-3")
	if err != nil {
		t.Fatalf("load fork session: %v", err)
	}
	if s.ID != "chat-1-task-3" || len(s.Messages) != 2 || s.Messages[1].Content != "sub out" {
		t.Fatalf("fork session: %+v", s)
	}

	// 主循环照常写主文件，不覆盖分身。
	if _, err := core.NewAgent(
		testutil.Scripted(testutil.Text("main out")),
		core.WithHooks(ls),
	).Run(context.Background(), "main"); err != nil {
		t.Fatalf("main run: %v", err)
	}
	ids, err := List(context.Background(), fsys, "")
	if err != nil || len(ids) != 2 {
		t.Fatalf("list: %v err=%v", ids, err)
	}
	m, err := Load(context.Background(), fsys, "", "chat-1")
	if err != nil || len(m.Messages) != 2 || m.Messages[1].Content != "main out" {
		t.Fatalf("main session: %+v err=%v", m, err)
	}
}

// 分身快照只存增量：seed 是主上下文的逐字节重复，剥离后第一条即分身 input。
func TestForkSessionStoresIncrementOnly(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	ls := New(fsys, "chat-1")
	seed := []types.Message{
		{Role: types.RoleSystem, Content: "sys"},
		{Role: types.RoleUser, Content: "background"},
	}
	if _, err := core.NewAgent(
		testutil.Scripted(testutil.Text("sub out")),
		core.WithHooks(ls),
	).Fork(context.Background(), "task-5", seed, nil, "subtask"); err != nil {
		t.Fatalf("fork: %v", err)
	}
	s, err := Load(context.Background(), fsys, "", "chat-1-task-5")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(s.Messages) != 2 || s.Messages[0].Content != "subtask" || s.Messages[1].Content != "sub out" {
		t.Fatalf("fork session should be increment only: %+v", s.Messages)
	}
}

func TestAutoIDAndMessageJSON(t *testing.T) {
	h := New(fs.NewLocal(t.TempDir()), "")
	if h.ID() == "" {
		t.Fatal("auto id must not be empty")
	}
	msg := types.Message{Role: types.RoleTool, ToolCallID: "c1", Content: "x", Err: "e"}
	b, err := json.Marshal(msg)
	if err != nil || !strings.Contains(string(b), `"tool_call_id":"c1"`) {
		t.Fatalf("message json: %s %v", b, err)
	}
}
