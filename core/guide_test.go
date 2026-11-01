package core

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

/* guideHook 是 Guide 声明的测试替身：返回固定文本，可空串。 */
type guideHook struct {
	name string
	text string
}

func (g guideHook) Name() string  { return g.name }
func (g guideHook) Guide() string { return g.text }

// 组装期统一追加：startHook 全部跑完后按注册序拼到 system 尾部。
// system 作者（重写 Messages[0] 的 hook）注册在声明者之后也不会擦掉附录。
func TestGuideAppendedAfterStartHooks(t *testing.T) {
	sys := sysWriter{prompt: "base"}
	state, err := NewAgent(
		testutil.Scripted(testutil.Text("done")),
		WithHooks(
			guideHook{name: "g1", text: "guide-1"},
			guideHook{name: "g2", text: "guide-2"},
			sys, // 重写 system 的 hook 排在最后：声明仍须存活
		),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	sysMsg := state.Messages[0]
	if sysMsg.Role != types.RoleSystem {
		t.Fatalf("first message should be system: %+v", sysMsg)
	}
	want := "base\n\nguide-1\n\nguide-2"
	if sysMsg.Content != want {
		t.Fatalf("system = %q, want %q", sysMsg.Content, want)
	}
}

// system 缺席且有声明时创建（guide 依附 system；filetools 的 OS 提示在
// 无 system 裸装配同样成立）；声明全为空串时不创建、不追加。
func TestGuideCreatesSystemWhenAbsent(t *testing.T) {
	state, err := NewAgent(
		testutil.Scripted(testutil.Text("done")),
		WithHooks(guideHook{name: "g", text: "os-hint"}),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Messages[0].Role != types.RoleSystem || state.Messages[0].Content != "os-hint" {
		t.Fatalf("system should be created from guides: %+v", state.Messages[0])
	}

	state2, err := NewAgent(
		testutil.Scripted(testutil.Text("done")),
		WithHooks(guideHook{name: "g", text: ""}),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, m := range state2.Messages {
		if m.Role == types.RoleSystem {
			t.Fatal("empty guide must not create system message")
		}
	}
}

// 跨 Run 幂等：WithHistory 过滤历史 system，引擎重组装恰追加一次，不滚雪球。
func TestGuideIdempotentAcrossRuns(t *testing.T) {
	a := NewAgent(
		testutil.Scripted(testutil.Text("done"), testutil.Text("done")),
		WithSystemPrompt("base"),
		WithHooks(guideHook{name: "g", text: "guide"}),
	)
	s1, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	s2, err := a.Run(context.Background(), "again", WithHistory(s1.Messages...))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got := strings.Count(s2.Messages[0].Content, "guide"); got != 1 {
		t.Fatalf("guide should appear exactly once, got %d: %q", got, s2.Messages[0].Content)
	}
	var systems int
	for _, m := range s2.Messages {
		if m.Role == types.RoleSystem {
			systems++
		}
	}
	if systems != 1 {
		t.Fatalf("single system message expected, got %d", systems)
	}
}

// fork 不重复追加：seed 已含 system（含 Guide 附录），子循环 guideHooks 置空。
func TestGuideNotDuplicatedInFork(t *testing.T) {
	a := NewAgent(
		testutil.Scripted(testutil.Text("done"), testutil.Text("sub done")),
		WithSystemPrompt("base"),
		WithHooks(guideHook{name: "g", text: "guide"}),
	)
	s1, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	sub, err := a.Fork(context.Background(), "f1", s1.Messages, nil, "sub task")
	if err != nil {
		t.Fatalf("fork err: %v", err)
	}
	if got := strings.Count(sub.Messages[0].Content, "guide"); got != 1 {
		t.Fatalf("fork must not duplicate guide, got %d: %q", got, sub.Messages[0].Content)
	}
}

/* sysWriter 是重写 system 的 startHook 替身（模拟宿主的 sysprompt hook）。 */
type sysWriter struct{ prompt string }

func (s sysWriter) Name() string { return "sys" }
func (s sysWriter) OnStart(_ context.Context, state *types.LoopState) error {
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content = s.prompt // 整体重写（幂等基线）
		return nil
	}
	state.Messages = append([]types.Message{{Role: types.RoleSystem, Content: s.prompt}}, state.Messages...)
	return nil
}
