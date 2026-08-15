package skill

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

func TestSkillInjectsOnKeywordMatch(t *testing.T) {
	h := New(
		Skill{Name: "sql", Instructions: "always use parameterized sql", Keywords: []string{"db", "数据库"}},
		Skill{Name: "deploy", Instructions: "confirm before deploy", Keywords: []string{"deploy"}},
	)
	state := &types.LoopState{
		Input:    "帮我查一下数据库里的用户",
		Messages: []types.Message{{Role: types.RoleUser, Content: "帮我查一下数据库里的用户"}},
	}
	if err := h.OnStart(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("messages: %d", len(state.Messages))
	}
	sys := state.Messages[0]
	if sys.Role != types.RoleSystem {
		t.Fatalf("first msg role: %s", sys.Role)
	}
	if !strings.Contains(sys.Content, "parameterized sql") {
		t.Fatalf("content: %q", sys.Content)
	}
	if strings.Contains(sys.Content, "deploy") {
		t.Fatal("unmatched skill should not be injected")
	}
}

func TestSkillNoMatchNoInject(t *testing.T) {
	h := New(Skill{Name: "sql", Instructions: "x", Keywords: []string{"db"}})
	state := &types.LoopState{Input: "闲聊", Messages: []types.Message{{Role: types.RoleUser, Content: "闲聊"}}}
	if err := h.OnStart(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("should not inject, got %d messages", len(state.Messages))
	}
}

func TestSkillNoKeywordsAlwaysInject(t *testing.T) {
	h := New(Skill{Name: "base", Instructions: "be concise"})
	state := &types.LoopState{Input: "anything", Messages: []types.Message{{Role: types.RoleUser, Content: "anything"}}}
	_ = h.OnStart(context.Background(), state)
	if len(state.Messages) != 2 {
		t.Fatalf("always-inject skill missing: %d", len(state.Messages))
	}
}
