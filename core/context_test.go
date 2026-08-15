package core

import (
	"context"
	"fmt"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

// captureProvider 记录收到的完整消息序列。
type captureProvider struct {
	last *types.ModelRequest
}

func (p *captureProvider) Invoke(_ context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	p.last = req
	return &types.ModelResponse{Content: "ok"}, nil
}

func TestSystemPromptAndHistoryOrder(t *testing.T) {
	cp := &captureProvider{}
	a := NewAgent(cp, WithSystemPrompt("你是一个严谨的助手"))

	history := []types.Message{
		{Role: types.RoleUser, Content: "第一轮问题"},
		{Role: types.RoleAssistant, Content: "第一轮回答"},
	}
	state, err := a.Run(context.Background(), "第二轮问题", WithHistory(history...))
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// state 与 provider 侧序列一致：system → history → input
	want := []struct{ role types.Role; content string }{
		{types.RoleSystem, "你是一个严谨的助手"},
		{types.RoleUser, "第一轮问题"},
		{types.RoleAssistant, "第一轮回答"},
		{types.RoleUser, "第二轮问题"},
		{types.RoleAssistant, "ok"},
	}
	if len(state.Messages) != len(want) {
		t.Fatalf("messages: %d, want %d", len(state.Messages), len(want))
	}
	for i, w := range want {
		m := state.Messages[i]
		if m.Role != w.role || m.Content != w.content {
			t.Fatalf("msg[%d] = %s/%q, want %s/%q", i, m.Role, m.Content, w.role, w.content)
		}
	}
	// provider 侧在 assistant 回复追加前调用，因此是前 4 条。
	if cp.last == nil || len(cp.last.Messages) != 4 {
		t.Fatalf("provider messages: %d", len(cp.last.Messages))
	}
}

// 多轮会话：上一轮返回的 state.Messages 可直接作为下一轮历史。
func TestMultiTurnConversation(t *testing.T) {
	cp := &captureProvider{}
	a := NewAgent(cp)

	s1, err := a.Run(context.Background(), "turn 1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 去掉末轮 assistant 回复，保留到 user 侧再补充？这里直接全量传递（OpenAI 兼容）。
	s2, err := a.Run(context.Background(), "turn 2", WithHistory(s1.Messages...))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// s2: turn1(user, assistant) + turn2(user, assistant)
	roles := []types.Role{}
	for _, m := range s2.Messages {
		roles = append(roles, m.Role)
	}
	want := []types.Role{types.RoleUser, types.RoleAssistant, types.RoleUser, types.RoleAssistant}
	if fmt.Sprint(roles) != fmt.Sprint(want) {
		t.Fatalf("roles: %v", roles)
	}
}
