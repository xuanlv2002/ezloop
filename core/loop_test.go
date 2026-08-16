package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

func collectEvents(a *Agent) *[]event.EventType {
	var evs []event.EventType
	a.onEvent = func(e event.Event) { evs = append(evs, e.Type) }
	return &evs
}

// 回边路由 + 消息序列 + 事件顺序：引擎最核心的行为。
func TestRunLoopbackAndEventOrder(t *testing.T) {
	a := NewAgent(testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", "echo", `{"m":"a"}`)),
		testutil.Text("done"),
	), WithTools(testutil.EchoTool{}))
	evs := collectEvents(a)

	state, err := a.Run(context.Background(), "hi")
	if err != nil || state.StopReason != types.StopCompleted || state.Iteration != 2 {
		t.Fatalf("state: %+v err=%v", state, err)
	}
	roles := []types.Role{}
	for _, m := range state.Messages {
		roles = append(roles, m.Role)
	}
	want := []types.Role{types.RoleUser, types.RoleAssistant, types.RoleTool, types.RoleAssistant}
	if fmt.Sprint(roles) != fmt.Sprint(want) {
		t.Fatalf("roles: %v", roles)
	}
	wantEv := []event.EventType{
		event.EventLoopStart,
		event.EventModelStart, event.EventModelEnd,
		event.EventToolStart, event.EventToolEnd,
		event.EventIterationEnd,
		event.EventModelStart, event.EventModelEnd,
		event.EventLoopEnd,
	}
	if fmt.Sprint(*evs) != fmt.Sprint(wantEv) {
		t.Fatalf("events: %v", *evs)
	}
}

// 并发工具执行 + 消息保序 + 单轮完成。
func TestConcurrentToolsPreserveOrder(t *testing.T) {
	tool := &sleepTool{delay: 30 * time.Millisecond}
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("c0", "sleep", `{}`),
				testutil.Call("c1", "sleep", `{}`),
				testutil.Call("c2", "sleep", `{}`),
				testutil.Call("c3", "sleep", `{}`),
			),
			testutil.Text("done"),
		),
		WithTools(tool),
		WithHyperParams(HyperParams{MaxConcurrency: 4}),
	)
	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if tool.maxSeen != 4 {
		t.Fatalf("want 4 concurrent, saw %d", tool.maxSeen)
	}
	for i, want := range []string{"c0", "c1", "c2", "c3"} {
		if state.Messages[2+i].ToolCallID != want {
			t.Fatalf("order broken at %d", i)
		}
	}
}

type sleepTool struct {
	mu      sync.Mutex
	inflight, maxSeen int
	delay   time.Duration
}

func (t *sleepTool) Name() string                { return "sleep" }
func (t *sleepTool) Description() string         { return "" }
func (t *sleepTool) ArgsSchema() json.RawMessage { return nil }
func (t *sleepTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	t.mu.Lock()
	t.inflight++
	if t.inflight > t.maxSeen {
		t.maxSeen = t.inflight
	}
	t.mu.Unlock()
	time.Sleep(t.delay)
	t.mu.Lock()
	t.inflight--
	t.mu.Unlock()
	return "done", nil
}

// ToolStart 短路：Skip 跳过并携带调用信息，Abort 终止。
func TestToolStartShortCircuit(t *testing.T) {
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("1", "echo", `{}`),
				testutil.Call("2", "echo", `{}`),
			),
			testutil.Text("done"),
		),
		WithTools(testutil.EchoTool{}),
		WithHooks(denyEcho{skip: true}),
	)
	state, _ := a.Run(context.Background(), "hi")
	if state.StopReason != types.StopCompleted {
		t.Fatalf("skip should continue loop: %s", state.StopReason)
	}
	if !strings.Contains(state.Messages[2].Content, "skipped by tool-start hook: echo") {
		t.Fatalf("skip msg: %q", state.Messages[2].Content)
	}

	a2 := NewAgent(
		testutil.Scripted(testutil.ToolCalls(testutil.Call("1", "echo", `{}`))),
		WithTools(testutil.EchoTool{}),
		WithHooks(denyEcho{skip: false}),
	)
	state2, _ := a2.Run(context.Background(), "hi")
	if state2.StopReason != types.StopAborted {
		t.Fatalf("abort: %s", state2.StopReason)
	}
}

type denyEcho struct{ skip bool }

func (denyEcho) Name() string { return "deny" }
func (h denyEcho) OnToolStart(_ context.Context, _ *types.LoopState, _ *types.ToolCall) (hook.Action, error) {
	if h.skip {
		return hook.ActionSkip, nil
	}
	return hook.ActionAbort, nil
}

// hook panic 被引擎恢复为带上下文的错误，EndHook 仍执行。
func TestHookErrorAndPanicProtection(t *testing.T) {
	endRan := false
	a := NewAgent(testutil.Scripted(testutil.Text("x")),
		WithHooks(panickyStart{endRan: &endRan}))
	state, err := a.Run(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("want panic error, got %v", err)
	}
	if state.StopReason != types.StopError || !endRan {
		t.Fatalf("stop=%s endRan=%v", state.StopReason, endRan)
	}
}

type panickyStart struct{ endRan *bool }

func (panickyStart) Name() string { return "panicky" }
func (panickyStart) OnModelStart(_ context.Context, _ *types.LoopState) error {
	panic("boom")
}
func (h panickyStart) OnEnd(_ context.Context, _ *types.LoopState) error {
	*h.endRan = true
	return nil
}

// ctx 取消 → StopCancelled。
func TestContextCancellation(t *testing.T) {
	block := testutil.NewBlocking()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	state, err := NewAgent(block).Run(ctx, "hi")
	if err == nil || state.StopReason != types.StopCancelled {
		t.Fatalf("stop=%s err=%v", state.StopReason, err)
	}
}

// RunAsync：事件通道 + Wait。
func TestRunAsync(t *testing.T) {
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "echo", `{}`)),
			testutil.Text("finished"),
		),
		WithTools(testutil.EchoTool{}),
	)
	h := a.RunAsync(context.Background(), "hi")
	var first, last event.EventType
	for e := range h.Events() {
		if first == "" {
			first = e.Type
		}
		last = e.Type
	}
	state, err := h.Wait()
	if err != nil || state.StopReason != types.StopCompleted {
		t.Fatalf("state: %s err=%v", state.StopReason, err)
	}
	if first != event.EventLoopStart || last != event.EventLoopEnd {
		t.Fatalf("events: %s..%s", first, last)
	}
}

// system prompt + 历史注入顺序。
func TestSystemPromptAndHistory(t *testing.T) {
	a := NewAgent(testutil.Scripted(testutil.Text("ok")),
		WithSystemPrompt("be strict"))
	state, err := a.Run(context.Background(), "q2",
		WithHistory(
			types.Message{Role: types.RoleUser, Content: "q1"},
			types.Message{Role: types.RoleAssistant, Content: "a1"},
		))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"be strict", "q1", "a1", "q2", "ok"}
	if len(state.Messages) != len(want) {
		t.Fatalf("messages: %d", len(state.Messages))
	}
	for i, w := range want {
		if state.Messages[i].Content != w {
			t.Fatalf("msg[%d]=%q want %q", i, state.Messages[i].Content, w)
		}
	}
}

// 历史快照中的 system 消息（agent prompt / skill 注入）不重复带入：
// 多轮对话每轮恰好一条 system。
func TestHistorySystemNotDuplicated(t *testing.T) {
	snapshot := []types.Message{
		{Role: types.RoleSystem, Content: "be strict"},
		{Role: types.RoleSystem, Content: "# skill: sql"},
		{Role: types.RoleUser, Content: "q1"},
		{Role: types.RoleAssistant, Content: "a1"},
	}
	state, err := NewAgent(testutil.Scripted(testutil.Text("ok")),
		WithSystemPrompt("be strict")).Run(context.Background(), "q2",
		WithHistory(snapshot...))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	sysCount := 0
	for _, m := range state.Messages {
		if m.Role == types.RoleSystem {
			sysCount++
		}
	}
	if sysCount != 1 || state.Messages[0].Content != "be strict" {
		t.Fatalf("system messages: %d", sysCount)
	}
	if len(state.Messages) != 5 { // system + q1 + a1 + q2 + ok
		t.Fatalf("messages: %d", len(state.Messages))
	}
}

// ToolWarp 覆盖静态注册与 hook 注入的工具。
type tagWrap struct{ log *[]string }

func (w tagWrap) warp(inner types.Tool) types.Tool {
	return &taggedTool{inner: inner, log: w.log}
}

type taggedTool struct {
	inner types.Tool
	log   *[]string
}

func (t *taggedTool) Name() string                { return t.inner.Name() }
func (t *taggedTool) Description() string         { return t.inner.Description() }
func (t *taggedTool) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }
func (t *taggedTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	*t.log = append(*t.log, "w:"+t.inner.Name())
	return t.inner.Invoke(ctx, args)
}

type injectHook struct{ tool types.Tool }

func (injectHook) Name() string { return "inject" }
func (h injectHook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(h.tool)
	return nil
}

type renamedEcho struct{ testutil.EchoTool }

func (renamedEcho) Name() string { return "injected" }

func TestToolWarpWrapsAllSources(t *testing.T) {
	var log []string
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("1", "echo", `{}`),
				testutil.Call("2", "injected", `{}`),
			),
			testutil.Text("done"),
		),
		WithTools(testutil.EchoTool{}),
		WithHooks(injectHook{tool: renamedEcho{}}),
		WithToolWarp(tagWrap{log: &log}.warp),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if fmt.Sprint(log) != fmt.Sprint([]string{"w:echo", "w:injected"}) {
		t.Fatalf("log: %v", log)
	}
}

// 自定义事件经 state.EmitEvent 到达 OnEvent。
type emitHook struct{}

func (emitHook) Name() string { return "emit" }
func (emitHook) OnModelEnd(_ context.Context, s *types.LoopState) error {
	s.EmitEvent("custom.test", "payload")
	return nil
}

func TestHookEmitsCustomEvent(t *testing.T) {
	got := 0
	a := NewAgent(testutil.Scripted(testutil.Text("ok")),
		WithHooks(emitHook{}),
		WithOnEvent(func(e event.Event) {
			if e.Type == "custom.test" && e.Iteration == 1 {
				got++
			}
		}))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 1 {
		t.Fatalf("custom events: %d", got)
	}
}
