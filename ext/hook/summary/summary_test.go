package summary

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

type echoProvider struct {
	prefix string
	err    error
}

func (p echoProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &types.ModelResponse{Content: p.prefix + " summary"}, nil
}

func TestSummaryWritesMetadata(t *testing.T) {
	h := New(echoProvider{prefix: "good"}, "")
	state := &types.LoopState{Metadata: map[string]any{}, Messages: []types.Message{
		{Role: types.RoleUser, Content: "do stuff"},
		{Role: types.RoleAssistant, Content: "done"},
	}}
	if err := h.OnEnd(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	got, _ := state.Metadata["summary"].(string)
	if !strings.Contains(got, "good summary") {
		t.Fatalf("summary: %q", got)
	}
}

func TestSummaryFailureDoesNotBlock(t *testing.T) {
	h := New(echoProvider{err: assertErr("model down")}, "")
	state := &types.LoopState{Metadata: map[string]any{}}
	if err := h.OnEnd(context.Background(), state); err != nil {
		t.Fatalf("summary failure must not propagate: %v", err)
	}
	if state.Metadata["summary_error"] == nil {
		t.Fatal("want summary_error recorded")
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
