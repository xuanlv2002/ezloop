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
}

func (p stubProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
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
	if err := New(stubProvider{content: "GOOD"}, "").OnEnd(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	if s, _ := state.Metadata["summary"].(string); !strings.Contains(s, "GOOD") {
		t.Fatalf("summary: %v", state.Metadata["summary"])
	}

	state2 := &types.LoopState{Metadata: map[string]any{}}
	if err := New(stubProvider{err: errStr("down")}, "").OnEnd(context.Background(), state2); err != nil {
		t.Fatalf("failure must not propagate: %v", err)
	}
	if state2.Metadata["summary_error"] == nil {
		t.Fatal("want summary_error")
	}
}

type errStr string

func (e errStr) Error() string { return string(e) }
