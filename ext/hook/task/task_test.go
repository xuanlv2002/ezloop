package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// 委派 → 子循环完成 → 最终答案作为工具结果进入主历史，主循环继续。
func TestTaskDelegationReturnsResult(t *testing.T) {
	sub := testutil.Scripted(testutil.Text("sub result"))
	h := New(sub)

	var sawStart, sawEnd bool
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"count files"}`)),
			testutil.Text("done"),
		),
		core.WithHooks(h),
		core.WithOnEvent(func(e event.Event) {
			switch e.Type {
			case EventStart:
				sawStart = true
			case EventEnd:
				sawEnd = true
			}
		}),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if !sawStart || !sawEnd {
		t.Fatalf("events start=%v end=%v", sawStart, sawEnd)
	}
	if state.Messages[2].Role != types.RoleTool || state.Messages[2].ToolCallID != "1" {
		t.Fatalf("msg shape: %+v", state.Messages[2])
	}
	if state.Messages[2].Content != "sub result" {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
	if state.Messages[3].Content != "done" {
		t.Fatalf("parent final: %q", state.Messages[3].Content)
	}
}

// WithHooks 即可：task 工具由 OnStart 自动注册，无需 core.WithTools(Tool())。
func TestHookRegistersToolOnStart(t *testing.T) {
	h := New(testutil.Scripted())
	state := &types.LoopState{Tools: types.NewToolRegistry()}
	if err := h.OnStart(context.Background(), state); err != nil {
		t.Fatalf("OnStart: %v", err)
	}
	if _, err := state.Tools.Lookup(ToolName); err != nil {
		t.Fatalf("task tool not auto-registered: %v", err)
	}
}

// 子 Agent 默认继承父 state 的工具：子循环能调用父工具完成子任务。
func TestTaskInheritsParentTools(t *testing.T) {
	rec := &recTool{}
	sub := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("s1", "rec", `{"v":"x"}`)),
		testutil.Text("sub done"),
	)
	h := New(sub) // InheritTools 默认 true

	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"go"}`)),
			testutil.Text("final"),
		),
		core.WithTools(rec),
		core.WithHooks(h),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if rec.calls != 1 {
		t.Fatalf("sub should invoke inherited tool once, got %d", rec.calls)
	}
	if state.Messages[2].Content != "sub done" {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
}

// 继承工具时剔除 task 自身，防止子 Agent 无界递归。
func TestInheritedToolsFiltersTask(t *testing.T) {
	state := &types.LoopState{Tools: types.NewToolRegistry()}
	state.Tools.Register(testutil.EchoTool{})
	state.Tools.Register(Tool())

	got := inheritedTools(state)
	if len(got) != 1 || got[0].Name() != "echo" {
		t.Fatalf("inherited tools: %v", toolNames(got))
	}
}

// 子循环出错不终止主循环：错误作为工具结果回传，主模型自纠。
func TestTaskSubAgentErrorBecomesResult(t *testing.T) {
	h := New(errProvider{})
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"boom"}`)),
			testutil.Text("recovered"),
		),
		core.WithHooks(h),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("sub error must not abort parent: %s", state.StopReason)
	}
	if !strings.Contains(state.Messages[2].Content, "task failed") {
		t.Fatalf("result should carry sub error: %q", state.Messages[2].Content)
	}
	if state.Messages[3].Content != "recovered" {
		t.Fatalf("parent should continue: %q", state.Messages[3].Content)
	}
}

// 子循环用量累加到父 state.Usage。
func TestTaskAccumulatesUsage(t *testing.T) {
	h := New(usageProvider{})
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"go"}`)),
			testutil.Text("done"),
		),
		core.WithHooks(h),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Usage.PromptTokens != 3 || state.Usage.CompletionTokens != 2 {
		t.Fatalf("usage: %+v", state.Usage)
	}
}

// 空子任务描述 → 直接以提示文案作为结果，不派生子循环。
func TestTaskEmptyDescription(t *testing.T) {
	h := New(testutil.Scripted())
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{}`)),
			testutil.Text("done"),
		),
		core.WithHooks(h),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(state.Messages[2].Content, "empty subtask") {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
}

// 非目标工具直接放行。
func TestTaskIgnoresOtherTools(t *testing.T) {
	h := New(testutil.Scripted())
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

// 未挂 Hook 时壳工具报错，装配问题尽早暴露。
func TestToolWithoutHookFails(t *testing.T) {
	if _, err := Tool().Invoke(context.Background(), nil); err == nil {
		t.Fatal("want error when hook not registered")
	}
}

// recTool 记录被调用次数，验证子 Agent 真的走到了继承工具。
type recTool struct{ calls int }

func (r *recTool) Name() string                { return "rec" }
func (r *recTool) Description() string         { return "recording tool" }
func (r *recTool) ArgsSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *recTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	r.calls++
	return "recorded", nil
}

// errProvider 总是返回错误，模拟子模型故障。
type errProvider struct{}

func (errProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	return nil, errors.New("sub model down")
}

// usageProvider 返回带用量的固定响应。
type usageProvider struct{}

func (usageProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	return &types.ModelResponse{Content: "sub", Usage: types.Usage{PromptTokens: 3, CompletionTokens: 2}}, nil
}

func toolNames(tools []types.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}
