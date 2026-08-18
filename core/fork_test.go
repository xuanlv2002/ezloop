package core

import (
	"context"
	"sync"
	"testing"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// startCounter 记录 OnStart 是否执行，验证 fork 跳过组装期 hook。
type startCounter struct{ ran int }

func (h *startCounter) Name() string { return "startCounter" }
func (h *startCounter) OnStart(_ context.Context, _ *types.LoopState) error {
	h.ran++
	return nil
}

// skipTool 模拟审批类 hook：拦截指定工具（Skip），验证运行期 hook 在子循环生效。
type skipTool struct {
	name    string
	skipped int
}

func (h *skipTool) Name() string { return "skipTool" }
func (h *skipTool) OnToolStart(_ context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name == h.name {
		h.skipped++
		return hook.Skip("blocked by guard"), nil
	}
	return hook.Proceed, nil
}

// emitOnModelEnd 在子循环里经 state.EmitEvent 发自定义事件，验证 hook 事件也带 TaskID。
type emitOnModelEnd struct{}

func (emitOnModelEnd) Name() string { return "emitOnModelEnd" }
func (emitOnModelEnd) OnModelEnd(_ context.Context, state *types.LoopState) error {
	state.EmitEvent("forktest.custom", nil)
	return nil
}

// 运行期 hook 全继承：toolStart 拦截在 fork 子循环内照常生效（审批无旁路）。
func TestForkInheritsToolStartHook(t *testing.T) {
	guard := &skipTool{name: "echo"}
	a := NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("s1", "echo", `{}`)),
			testutil.Text("sub done"),
		),
		WithHooks(guard),
		WithTools(testutil.EchoTool{}),
	)

	sub, err := a.Fork(context.Background(), "t", nil, nil, "go")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if guard.skipped != 1 {
		t.Fatalf("guard should intercept inside fork, got %d", guard.skipped)
	}
	var toolMsg string
	for _, m := range sub.Messages {
		if m.Role == types.RoleTool {
			toolMsg = m.Content
		}
	}
	if toolMsg != "blocked by guard" {
		t.Fatalf("tool result should be guard's skip text: %q", toolMsg)
	}
}

// 事件带身份：主循环事件 TaskID 为空，fork 子循环内引擎事件与 hook 经
// EmitEvent 发的事件一律带上 taskID。
func TestForkEventsCarryTaskID(t *testing.T) {
	var mu sync.Mutex
	var events []event.Event
	collect := func(e event.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}
	a := NewAgent(
		testutil.Scripted(testutil.Text("main"), testutil.Text("ok")),
		WithOnEvent(collect),
		WithHooks(emitOnModelEnd{}),
	)
	if _, err := a.Run(context.Background(), "main"); err != nil {
		t.Fatalf("main run: %v", err)
	}
	sub, err := a.Fork(context.Background(), "task-9", nil, nil, "go")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if sub.TaskID != "task-9" {
		t.Fatalf("sub.TaskID: %q", sub.TaskID)
	}

	mu.Lock()
	defer mu.Unlock()
	inFork := false
	sawCustom := false
	for _, e := range events {
		if e.TaskID == "task-9" {
			inFork = true
			if e.Type == "forktest.custom" {
				sawCustom = true
			}
			continue
		}
		if e.TaskID != "" {
			t.Fatalf("unexpected taskId %q on %s", e.TaskID, e.Type)
		}
	}
	if !inFork || !sawCustom {
		t.Fatalf("fork events missing: inFork=%v custom=%v", inFork, sawCustom)
	}
}

// 组装期 hook 不重跑：OnStart 未执行；system 只保留 seed 中那份，
// Agent 的 systemPrompt 不再注入。
func TestForkSkipsStartHooks(t *testing.T) {
	sc := &startCounter{}
	a := NewAgent(
		testutil.Scripted(testutil.Text("ok")),
		WithSystemPrompt("MAIN"),
		WithHooks(sc),
	)
	seed := []types.Message{
		{Role: types.RoleSystem, Content: "SEED"},
		{Role: types.RoleUser, Content: "q"},
	}
	sub, err := a.Fork(context.Background(), "t", seed, nil, "go")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if sc.ran != 0 {
		t.Fatalf("start hook must not run inside fork, ran %d", sc.ran)
	}
	sysCount, sysContent := 0, ""
	for _, m := range sub.Messages {
		if m.Role == types.RoleSystem {
			sysCount++
			sysContent = m.Content
		}
	}
	if sysCount != 1 || sysContent != "SEED" {
		t.Fatalf("system should be exactly the seed one: count=%d content=%q", sysCount, sysContent)
	}
}

// seed 深拷贝：fork 之后父消息切片再追加，不影响子循环已快照的历史
// （共享底层数组时父写入会踩到子的消息）。
func TestForkSeedIsolation(t *testing.T) {
	a := NewAgent(testutil.Scripted(testutil.Text("ok")))
	seed := make([]types.Message, 0, 10) // 预留容量，暴露共享底层数组的踩踏
	seed = append(seed,
		types.Message{Role: types.RoleUser, Content: "q1"},
		types.Message{Role: types.RoleAssistant, Content: "a1"},
	)
	sub, err := a.Fork(context.Background(), "t", seed, nil, "go")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	seed = append(seed, types.Message{Role: types.RoleUser, Content: "parent-later"})
	for _, m := range sub.Messages {
		if m.Content == "parent-later" {
			t.Fatal("parent append leaked into fork history: seed not deep-copied")
		}
	}
}

// SeedLen 标记 seed 边界：持久化层据此剥离，分身只存增量。
func TestForkSetsSeedLen(t *testing.T) {
	a := NewAgent(testutil.Scripted(testutil.Text("ok")))
	seed := []types.Message{
		{Role: types.RoleSystem, Content: "SEED"},
		{Role: types.RoleUser, Content: "q"},
	}
	sub, err := a.Fork(context.Background(), "t", seed, nil, "go")
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if sub.SeedLen != len(seed) {
		t.Fatalf("SeedLen: want %d got %d", len(seed), sub.SeedLen)
	}
	// 边界后第一条是分身自己的 input，不是 seed。
	if sub.Messages[sub.SeedLen].Content != "go" {
		t.Fatalf("first message after seed: %+v", sub.Messages[sub.SeedLen])
	}
}
