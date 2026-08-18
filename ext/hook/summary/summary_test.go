package summary

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

type stubProvider struct {
	content string
	err     error
	calls   int
}

func (p *stubProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	if p.err != nil {
		return nil, p.err
	}
	return &types.ModelResponse{Content: p.content}, nil
}

// 摘要写入 Metadata；失败不阻断主流程。
func TestSummary(t *testing.T) {
	state := &types.LoopState{Metadata: map[string]any{}, Messages: []types.Message{
		{Role: types.RoleUser, Content: "do"},
	}}
	if err := New(&stubProvider{content: "GOOD"}).OnEnd(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s, _ := state.Metadata["summary"].(string); !strings.Contains(s, "GOOD") {
		t.Fatalf("summary: %v", state.Metadata["summary"])
	}

	state2 := &types.LoopState{Metadata: map[string]any{}, Messages: state.Messages}
	if err := New(&stubProvider{err: errStr("down")}).OnEnd(context.Background(), state2); err != nil {
		t.Fatalf("failure must not propagate: %v", err)
	}
	if state2.Metadata["summary_error"] == nil {
		t.Fatal("want summary_error")
	}
}

// MinMessages 阈值：短会话直接跳过，不为摘要付一次模型调用。
func TestSummarySkipsShortHistory(t *testing.T) {
	p := &stubProvider{content: "GOOD"}
	state := &types.LoopState{Metadata: map[string]any{}, Messages: []types.Message{
		{Role: types.RoleUser, Content: "do"},
		{Role: types.RoleAssistant, Content: "done"},
	}}
	if err := New(p, WithMinMessages(5)).OnEnd(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	if p.calls != 0 {
		t.Fatalf("short history must skip summarize, calls=%d", p.calls)
	}
	if _, ok := state.Metadata["summary"]; ok {
		t.Fatal("no summary expected")
	}
}

// Summarize 可独立调用（手动/按需触发场景），自定义 prompt 生效。
func TestSummarizeStandalone(t *testing.T) {
	p := &stubProvider{content: "OVERVIEW"}
	s, err := Summarize(context.Background(), p, []types.Message{
		{Role: types.RoleUser, Content: "q"},
	}, "两句话总结")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if s != "OVERVIEW" || p.calls != 1 {
		t.Fatalf("s=%q calls=%d", s, p.calls)
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
