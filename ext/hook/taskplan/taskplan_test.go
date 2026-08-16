package taskplan

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// 三种处置 → 三种结果文案，模型据此继续/终止/修订。
func TestTaskPlanDecisions(t *testing.T) {
	cases := []struct {
		name   string
		d      Decision
		wantIn string
	}{
		{"execute", Decision{Kind: Execute}, "plan approved by user, execute as planned"},
		{"execute with note", Decision{Kind: Execute, Input: "skip tests"}, "note: skip tests"},
		{"reject", Decision{Kind: Reject, Input: "budget cut"}, "plan rejected by user: budget cut"},
		{"revise", Decision{Kind: Revise, Input: "先做二期"}, "plan needs revision, user feedback: 先做二期"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, decisions := New()
			go func() { decisions <- Decision{CallID: "1", Kind: tc.d.Kind, Input: tc.d.Input} }()

			state, err := core.NewAgent(
				testutil.Scripted(
					testutil.ToolCalls(testutil.Call("1", ToolName, `{"plan":"step1; step2"}`)),
					testutil.Text("done"),
				),
				core.WithTools(Tool()),
				core.WithHooks(h),
			).Run(context.Background(), "build")
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if state.Messages[2].Role != types.RoleTool || state.Messages[2].ToolCallID != "1" {
				t.Fatalf("msg shape: %+v", state.Messages[2])
			}
			if !strings.Contains(state.Messages[2].Content, tc.wantIn) {
				t.Fatalf("result: %q want contains %q", state.Messages[2].Content, tc.wantIn)
			}
		})
	}
}

// Revise 闭环：修订后模型重新提交，再次进入等待。
func TestTaskPlanResubmitLoop(t *testing.T) {
	h, decisions := New()
	go func() {
		decisions <- Decision{Kind: Revise, Input: "加一步压测"}
		decisions <- Decision{Kind: Execute}
	}()

	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{"plan":"v1"}`)),
			testutil.ToolCalls(testutil.Call("2", ToolName, `{"plan":"v2 加压测"}`)),
			testutil.Text("done"),
		),
		core.WithTools(Tool()),
		core.WithHooks(h),
	).Run(context.Background(), "build")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Iteration != 3 {
		t.Fatalf("iterations: %d", state.Iteration)
	}
}

// 非目标工具直接放行。
func TestTaskPlanIgnoresOtherTools(t *testing.T) {
	h, _ := New()
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "echo", `{}`)),
			testutil.Text("done"),
		),
		core.WithTools(testutil.EchoTool{}),
		core.WithHooks(h),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
}
