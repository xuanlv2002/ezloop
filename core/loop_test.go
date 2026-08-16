package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
	mu                sync.Mutex
	inflight, maxSeen int
	delay             time.Duration
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
		return hook.Skip(""), nil
	}
	return hook.Abort, nil
}

// Skip 携带结果文案 → 结果消息使用该文案（拒绝理由、用户回答等）。
type prefillDeny struct{}

func (prefillDeny) Name() string { return "prefill" }
func (prefillDeny) OnToolStart(_ context.Context, _ *types.LoopState, _ *types.ToolCall) (hook.Action, error) {
	return hook.Skip("user says no: protected dir"), nil
}

func TestSkipPrefilledResult(t *testing.T) {
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "echo", `{}`)),
			testutil.Text("done"),
		),
		WithTools(testutil.EchoTool{}),
		WithHooks(prefillDeny{}),
	)
	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Messages[2].Content != "user says no: protected dir" {
		t.Fatalf("prefilled msg: %q", state.Messages[2].Content)
	}
}

// abort / hook 报错后未执行的调用也补齐结果消息：历史无悬空 tool_call，
// 协议完整（持久化恢复安全）。
func TestExitPathsKeepHistoryComplete(t *testing.T) {
	script := func() *testutil.ScriptedProvider {
		return testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("1", "echo", `{}`),
				testutil.Call("2", "echo", `{}`),
				testutil.Call("3", "echo", `{}`),
			),
			testutil.Text("done"),
		)
	}

	state, _ := NewAgent(script(), WithTools(testutil.EchoTool{}),
		WithHooks(denyEcho{skip: false})).Run(context.Background(), "hi")
	if state.StopReason != types.StopAborted {
		t.Fatalf("abort stop: %s", state.StopReason)
	}
	if len(state.Messages) != 5 { // user + assistant + 3 tool
		t.Fatalf("abort history: %d messages", len(state.Messages))
	}
	for i, m := range state.Messages[2:] {
		if m.Role != types.RoleTool || m.Content != "not executed: echo" {
			t.Fatalf("abort msg[%d]: %+v", i, m)
		}
	}

	state2, err := NewAgent(script(), WithTools(testutil.EchoTool{}),
		WithHooks(errToolStart{})).Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want hook error")
	}
	if len(state2.Messages) != 5 {
		t.Fatalf("hook-err history: %d messages", len(state2.Messages))
	}
	for i, m := range state2.Messages[2:] {
		if m.Role != types.RoleTool || m.Content != "not executed: echo" {
			t.Fatalf("hook-err msg[%d]: %+v", i, m)
		}
	}
}

type errToolStart struct{}

func (errToolStart) Name() string { return "errts" }
func (errToolStart) OnToolStart(_ context.Context, _ *types.LoopState, _ *types.ToolCall) (hook.Action, error) {
	return hook.Proceed, fmt.Errorf("boom")
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

// 取消归类统一：hook 因 ctx 取消而报错 → 同样归类 cancelled；
// EndHook 拿到脱离取消的 ctx（清理动作不失败）。
type ctxErrStart struct{ endSawLiveCtx *bool }

func (ctxErrStart) Name() string { return "ctxerr" }
func (h ctxErrStart) OnStart(ctx context.Context, _ *types.LoopState) error {
	<-ctx.Done()
	return ctx.Err()
}
func (h ctxErrStart) OnEnd(ctx context.Context, _ *types.LoopState) error {
	*h.endSawLiveCtx = ctx.Err() == nil
	return nil
}

func TestCancelClassificationUnified(t *testing.T) {
	endSawLiveCtx := false
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()

	state, err := NewAgent(testutil.Scripted(), WithHooks(ctxErrStart{&endSawLiveCtx})).
		Run(ctx, "hi")
	if err == nil || state.StopReason != types.StopCancelled {
		t.Fatalf("stop=%s err=%v", state.StopReason, err)
	}
	if !endSawLiveCtx {
		t.Fatal("EndHook must observe a live (non-cancelled) ctx")
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

type constProvider struct {
	calls atomic.Int32
}

func (p *constProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls.Add(1)
	return &types.ModelResponse{Content: "ok"}, nil
}

// 同一 Agent 并发 Run 的钉子测试（-race 下运行钉住"Agent 构建后只读"约定）。
// RunAsync 的浅拷贝、共享 hook 列表、共享 provider 都依赖此约定。
func TestAgentConcurrentRuns(t *testing.T) {
	p := &constProvider{}
	a := NewAgent(p, WithOnEvent(func(event.Event) {}))
	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, err := a.Run(context.Background(), fmt.Sprintf("q%d", i))
			if err != nil || state.StopReason != types.StopCompleted {
				t.Errorf("run %d: stop=%s err=%v", i, state.StopReason, err)
			}
		}()
	}
	wg.Wait()
	if got := p.calls.Load(); got != n {
		t.Fatalf("provider calls: %d want %d", got, n)
	}
}

// streaming 开启但 provider 无 Stream 能力 → 发 stream_fallback 警告事件，不静默。
func TestStreamFallbackWarning(t *testing.T) {
	saw := false
	a := NewAgent(&constProvider{},
		WithStreaming(true),
		WithOnEvent(func(e event.Event) {
			if e.Type == event.EventStreamFallback {
				saw = true
			}
		}))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !saw {
		t.Fatal("want stream_fallback event when provider lacks Stream")
	}
}

// provider/warp 经 ctx 出口发的事件流经引擎事件流，且带迭代号。
type emittingProvider struct{}

func (p *emittingProvider) Invoke(ctx context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	event.EmitEvent(ctx, "provider.custom", "inner-event")
	return &types.ModelResponse{Content: "ok"}, nil
}

func TestProviderEventsFlowThroughCtx(t *testing.T) {
	var mu sync.Mutex
	var saw bool
	var iter int
	a := NewAgent(&emittingProvider{}, WithOnEvent(func(e event.Event) {
		if e.Type != "provider.custom" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		saw, iter = true, e.Iteration
	}))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !saw || iter != 1 {
		t.Fatalf("saw=%v iter=%d", saw, iter)
	}
}

// tool warp 经 ctx 出口发事件：到达 OnEvent；并发工具下出口被并发调用，
// 收集方自行加锁（并发安全是使用方的契约责任，-race 钉住引擎侧无竞争）。
type emitWarp struct{ inner types.Tool }

func (t emitWarp) Name() string                { return t.inner.Name() }
func (t emitWarp) Description() string         { return t.inner.Description() }
func (t emitWarp) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }
func (t emitWarp) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	event.EmitEvent(ctx, "toolwarp.custom", t.inner.Name())
	return t.inner.Invoke(ctx, args)
}

func TestToolWarpEventsFlow(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(
				testutil.Call("1", "sleep", `{}`),
				testutil.Call("2", "sleep", `{}`),
				testutil.Call("3", "sleep", `{}`),
				testutil.Call("4", "sleep", `{}`),
			),
			testutil.Text("done"),
		),
		WithTools(&sleepTool{delay: 10 * time.Millisecond}),
		WithToolWarp(func(inner types.Tool) types.Tool { return emitWarp{inner} }),
		WithHyperParams(HyperParams{MaxConcurrency: 4}),
		WithOnEvent(func(e event.Event) {
			if e.Type != "toolwarp.custom" {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, e.Data.(string))
		}),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 4 {
		t.Fatalf("tool warp events: %v", seen)
	}
}
