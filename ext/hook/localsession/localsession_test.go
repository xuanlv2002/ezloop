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
