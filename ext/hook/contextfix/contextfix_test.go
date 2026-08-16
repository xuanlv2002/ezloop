package contextfix

import (
	"context"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// 残缺历史：assistant 的部分/全部 tool_call 缺结果 → 补占位。
func TestFixFillsMissingResults(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: "q1"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
			testutil.Call("a", "echo", `{}`),
			testutil.Call("b", "echo", `{}`),
		}},
		{Role: types.RoleTool, ToolCallID: "a", Content: "ran"}, // b 缺失
		{Role: types.RoleAssistant, Content: "half done"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
			testutil.Call("c", "echo", `{}`),
		}}, // c 全缺
	}

	fixed := Fix(msgs)
	if len(fixed) != 7 {
		t.Fatalf("len: %d", len(fixed))
	}
	// b 的占位紧随 assistant 之后（先于已有的 tool(a)）
	if fixed[2].Role != types.RoleTool || fixed[2].ToolCallID != "b" || fixed[2].Content != Placeholder {
		t.Fatalf("fixed[2]: %+v", fixed[2])
	}
	if fixed[3].ToolCallID != "a" || fixed[4].Content != "half done" {
		t.Fatalf("order broken: %+v %+v", fixed[3], fixed[4])
	}
	// c 的占位插在末尾
	if fixed[6].Role != types.RoleTool || fixed[6].ToolCallID != "c" {
		t.Fatalf("fixed[6]: %+v", fixed[6])
	}
}

// 完整历史原样返回（同一切片，不新建）。
func TestFixLeavesCompleteHistory(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: "q"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{testutil.Call("a", "echo", `{}`)}},
		{Role: types.RoleTool, ToolCallID: "a", Content: "ran"},
	}
	if got := Fix(msgs); len(got) != 3 {
		t.Fatalf("len: %d", len(got))
	}
}

// 孤儿 tool 消息（对应 assistant 已丢失）→ 删除。
func TestFixRemovesOrphanToolMessages(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleUser, Content: "q1"},
		{Role: types.RoleTool, ToolCallID: "ghost", Content: "ran"}, // assistant 丢失
		{Role: types.RoleAssistant, Content: "done"},
		{Role: types.RoleUser, Content: "q2"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{testutil.Call("b", "echo", `{}`)}},
		{Role: types.RoleTool, ToolCallID: "b", Content: "ran"},
	}
	fixed := Fix(msgs)
	if len(fixed) != 5 {
		t.Fatalf("len: %d", len(fixed))
	}
	for _, m := range fixed {
		if m.ToolCallID == "ghost" {
			t.Fatal("orphan tool message must be removed")
		}
	}
	// 双向混合：删孤儿的同时补缺失
	msgs2 := []types.Message{
		{Role: types.RoleTool, ToolCallID: "ghost", Content: ""},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{testutil.Call("x", "echo", `{}`)}},
	}
	fixed2 := Fix(msgs2)
	if len(fixed2) != 2 { // 删 ghost，assistant + 补 x 结果
		t.Fatalf("len: %d", len(fixed2))
	}
	if fixed2[1].ToolCallID != "x" || fixed2[1].Content != Placeholder {
		t.Fatalf("filled: %+v", fixed2[1])
	}
}

// 全链路：残缺历史经 WithHistory 注入，OnStart 修理后请求模型。
func TestFixViaAgentRun(t *testing.T) {
	history := []types.Message{
		{Role: types.RoleUser, Content: "q1"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{testutil.Call("x", "echo", `{}`)}},
	}
	state, err := core.NewAgent(
		testutil.Scripted(testutil.Text("ok")),
		core.WithHooks(New()),
	).Run(context.Background(), "q2", core.WithHistory(history...))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// system? 无 prompt → user q1, assistant, tool(x), user q2, assistant ok
	if len(state.Messages) != 5 {
		t.Fatalf("messages: %d", len(state.Messages))
	}
	if state.Messages[2].Role != types.RoleTool || state.Messages[2].ToolCallID != "x" {
		t.Fatalf("fixed msg: %+v", state.Messages[2])
	}
	if state.Messages[3].Content != "q2" {
		t.Fatalf("order broken: %+v", state.Messages[3])
	}
}
