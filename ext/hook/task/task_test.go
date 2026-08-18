package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// withTask 构建一个挂了 task hook 的 Agent，并附加 opts。
// 主 Agent 经 ctx 自动注入（core.AgentFromContext），无需组装期绑定。
func withTask(p provider.ModelProvider, opts ...core.Option) *core.Agent {
	return core.NewAgent(p, append([]core.Option{core.WithHooks(New())}, opts...)...)
}

// fork → 子循环完成 → 最终答案作为工具结果进入主历史，主循环继续。
// fork 复用主 Agent 的 provider：脚本在同一 provider 上按调用顺序交错消费。
func TestTaskForkReturnsResult(t *testing.T) {
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"count files"}`)),
		testutil.Text("sub result"),
		testutil.Text("done"),
	)

	var sawStart, sawEnd bool
	var taskIDs []string
	state, err := withTask(
		p,
		core.WithOnEvent(func(e event.Event) {
			switch e.Type {
			case EventStart:
				sawStart = true
				taskIDs = append(taskIDs, e.TaskID)
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
	if len(taskIDs) != 1 || taskIDs[0] == "" {
		t.Fatalf("task.start should carry taskId: %v", taskIDs)
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
	h := New()
	state := &types.LoopState{Tools: types.NewToolRegistry()}
	if err := h.OnStart(context.Background(), state); err != nil {
		t.Fatalf("OnStart: %v", err)
	}
	if _, err := state.Tools.Lookup(ToolName); err != nil {
		t.Fatalf("task tool not auto-registered: %v", err)
	}
}

// fork 继承父 state 的工具：子循环能调用父工具完成子任务。
func TestTaskInheritsParentTools(t *testing.T) {
	rec := &recTool{}
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"go"}`)),
		testutil.ToolCalls(testutil.Call("s1", "rec", `{"v":"x"}`)),
		testutil.Text("sub done"),
		testutil.Text("final"),
	)

	state, err := withTask(p, core.WithTools(rec)).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if rec.calls != 1 {
		t.Fatalf("fork should invoke inherited tool once, got %d", rec.calls)
	}
	if state.Messages[2].Content != "sub done" {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
}

// 继承工具时剔除 task 自身，防止 fork 再次隔离造成递归（单层保证）。
func TestInheritedToolsFiltersTask(t *testing.T) {
	state := &types.LoopState{Tools: types.NewToolRegistry()}
	state.Tools.Register(testutil.EchoTool{})
	state.Tools.Register(Tool())

	got := inheritedTools(state)
	if len(got) != 1 || got[0].Name() != "echo" {
		t.Fatalf("inherited tools: %v", toolNames(got))
	}
}

// 隔离核心属性：fork 内部的过程性上下文（中间工具调用）不进入主上下文，
// 只有最终答案回传。
func TestForkDoesNotLeakIntermediateContext(t *testing.T) {
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"go"}`)),
		testutil.ToolCalls(testutil.Call("s1", "echo", `{"v":"x"}`)), // fork 中间调用
		testutil.Text("final answer"),
		testutil.Text("done"),
	)

	state, err := withTask(p, core.WithTools(testutil.EchoTool{})).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	// 主历史只应看到最终答案，不含 fork 的 echo 中间结果。
	if state.Messages[2].Content != "final answer" {
		t.Fatalf("result should be fork final: %q", state.Messages[2].Content)
	}
	for _, m := range state.Messages {
		if strings.Contains(m.Content, "echo:") {
			t.Fatalf("fork intermediate context leaked into main: %+v", m)
		}
	}
}

// fork 出错不终止主循环：错误作为工具结果回传，主模型自纠。
func TestTaskForkErrorBecomesResult(t *testing.T) {
	state, err := withTask(&errOnSecondProvider{}).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("fork error must not abort parent: %s", state.StopReason)
	}
	if !strings.Contains(state.Messages[2].Content, "task failed") {
		t.Fatalf("result should carry fork error: %q", state.Messages[2].Content)
	}
	if state.Messages[3].Content != "recovered" {
		t.Fatalf("parent should continue: %q", state.Messages[3].Content)
	}
}

// fork 用量累加到父 state.Usage。
func TestTaskAccumulatesUsage(t *testing.T) {
	state, err := withTask(&usageProvider{}).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Usage.PromptTokens != 3 || state.Usage.CompletionTokens != 2 {
		t.Fatalf("usage: %+v", state.Usage)
	}
}

// 并发：同一轮多个 task 调用并行 fork，各自带独立 taskId。
func TestTaskForksConcurrently(t *testing.T) {
	var mu sync.Mutex
	taskIDs := map[string]bool{}
	state, err := withTask(
		&twoForkProvider{},
		core.WithOnEvent(func(e event.Event) {
			if e.Type != EventStart {
				return
			}
			mu.Lock()
			taskIDs[e.TaskID] = true
			mu.Unlock()
		}),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if len(taskIDs) != 2 {
		t.Fatalf("want 2 distinct fork taskIds, got %v", taskIDs)
	}
	// 两个 task 调用各自拿到结果。
	toolMsgs := 0
	for _, m := range state.Messages {
		if m.Role == types.RoleTool && m.ToolCallID != "" {
			toolMsgs++
			if m.Content != "done" {
				t.Fatalf("fork result: %q", m.Content)
			}
		}
	}
	if toolMsgs != 2 {
		t.Fatalf("want 2 tool results, got %d", toolMsgs)
	}
}

// 单层：fork 的工具集没有 task，模型再调 task 只会得到"工具不存在"，
// 不会递归 fork。
func TestForkCannotForkAgain(t *testing.T) {
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"go"}`)),
		testutil.ToolCalls(testutil.Call("s1", ToolName, `{"task":"nested"}`)), // fork 试图再 fork
		testutil.Text("after failed nested"),
		testutil.Text("done"),
	)
	state, err := withTask(p).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	// fork 的最终答案（nested 调用被拒后继续）。
	if state.Messages[2].Content != "after failed nested" {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
}

// 分身继承父 Agent 的全部运行期 hook：父的 toolStart 拦截在 fork 子循环内
// 照常生效（审批无旁路）。
func TestTaskForkRunsParentToolStartHooks(t *testing.T) {
	guard := &guardTool{}
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", ToolName, `{"task":"go"}`)),
		testutil.ToolCalls(testutil.Call("s1", "echo", `{"v":"x"}`)), // 分身内调 echo，被父 hook 拦
		testutil.Text("sub done"),
		testutil.Text("final"),
	)
	a := core.NewAgent(p, core.WithHooks(New(), guard), core.WithTools(testutil.EchoTool{}))

	state, err := a.Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if guard.skipped != 1 {
		t.Fatalf("parent toolStart hook should run inside fork, got %d", guard.skipped)
	}
	if state.Messages[2].Content != "sub done" {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
}

// 空任务描述 → 直接以提示文案作为结果，不 fork。
func TestTaskEmptyDescription(t *testing.T) {
	state, err := withTask(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{}`)),
			testutil.Text("done"),
		),
	).Run(context.Background(), "delegate")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(state.Messages[2].Content, "empty task") {
		t.Fatalf("result: %q", state.Messages[2].Content)
	}
}

// 非目标工具直接放行。
func TestTaskIgnoresOtherTools(t *testing.T) {
	state, err := withTask(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "echo", `{}`)),
			testutil.Text("done"),
		),
		core.WithTools(testutil.EchoTool{}),
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

// recTool 记录被调用次数，验证 fork 真的走到了继承工具。
type recTool struct{ calls int }

func (r *recTool) Name() string                { return "rec" }
func (r *recTool) Description() string         { return "recording tool" }
func (r *recTool) ArgsSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (r *recTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	r.calls++
	return "recorded", nil
}

// guardTool 模拟审批类 hook：Skip 掉 echo，验证分身不旁路父的拦截。
type guardTool struct{ skipped int }

func (g *guardTool) Name() string { return "guardTool" }
func (g *guardTool) OnToolStart(_ context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name == "echo" {
		g.skipped++
		return hook.Skip("blocked by parent guard"), nil
	}
	return hook.Proceed, nil
}

// errOnSecondProvider 第二次调用返回错误：父调用 → fork 出错 → 父自纠。
type errOnSecondProvider struct{ calls int }

func (p *errOnSecondProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	switch p.calls {
	case 1:
		return &types.ModelResponse{Content: "calling", ToolCalls: []types.ToolCall{
			{ID: "1", Name: ToolName, Args: json.RawMessage(`{"task":"boom"}`)},
		}}, nil
	case 2:
		return nil, errors.New("sub model down")
	default:
		return &types.ModelResponse{Content: "recovered"}, nil
	}
}

// usageProvider 第一次调用发 task，第二次（fork）带用量，之后纯文本。
type usageProvider struct{ calls int }

func (p *usageProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	switch p.calls {
	case 1:
		return &types.ModelResponse{Content: "calling", ToolCalls: []types.ToolCall{
			{ID: "1", Name: ToolName, Args: json.RawMessage(`{"task":"go"}`)},
		}}, nil
	case 2:
		return &types.ModelResponse{Content: "sub", Usage: types.Usage{PromptTokens: 3, CompletionTokens: 2}}, nil
	default:
		return &types.ModelResponse{Content: "done"}, nil
	}
}

// twoForkProvider 第一次调用发两个 task，之后纯文本；并发安全。
type twoForkProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *twoForkProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls == 1 {
		return &types.ModelResponse{Content: "calling", ToolCalls: []types.ToolCall{
			{ID: "1", Name: ToolName, Args: json.RawMessage(`{"task":"a"}`)},
			{ID: "2", Name: ToolName, Args: json.RawMessage(`{"task":"b"}`)},
		}}, nil
	}
	return &types.ModelResponse{Content: "done"}, nil
}

func toolNames(tools []types.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name())
	}
	return names
}
