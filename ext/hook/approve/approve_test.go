package approve

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

type scriptedProvider struct {
	script []*types.ModelResponse
	calls  int
}

func (p *scriptedProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	if p.calls >= len(p.script) {
		return &types.ModelResponse{}, nil
	}
	resp := p.script[p.calls]
	p.calls++
	return resp, nil
}

type echoTool struct{}

func (echoTool) Name() string                { return "echo" }
func (echoTool) Description() string         { return "" }
func (echoTool) ArgsSchema() json.RawMessage { return nil }
func (echoTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	return "ran " + string(args), nil
}

func denyRm(_ context.Context, call *types.ToolCall) (bool, error) {
	return call.Name != "rm", nil
}

func runWithApprover(t *testing.T, h *Hook) *types.LoopState {
	t.Helper()
	p := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{
			{ID: "1", Name: "rm", Args: json.RawMessage(`{"path":"x"}`)},
			{ID: "2", Name: "echo", Args: json.RawMessage(`{"a":1}`)},
		}},
		{Content: "finished"},
	}}
	a := core.NewAgent(p, core.WithTools(echoTool{}), core.WithHooks(h))
	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	return state
}

func TestApproveSkipsDeniedTool(t *testing.T) {
	state := runWithApprover(t, New(denyRm))
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	// rm 被跳过、echo 被执行。
	rmMsg, echoMsg := "", ""
	for _, m := range state.Messages {
		if m.Role != types.RoleTool {
			continue
		}
		if m.Content == "" && m.Err == "" {
			continue
		}
		if m.Err != "" || m.Content != "" {
			if m.Content == "skipped by tool-start hook" || m.Err != "" {
				rmMsg = m.Content
			} else {
				echoMsg = m.Content
			}
		}
	}
	if rmMsg != "skipped by tool-start hook" {
		t.Fatalf("rm should be skipped, got %q", rmMsg)
	}
	if echoMsg != `ran {"a":1}` {
		t.Fatalf("echo should run, got %q", echoMsg)
	}
}

func TestApproveAbortMode(t *testing.T) {
	state := runWithApprover(t, New(denyRm, hook.ActionAbort))
	if state.StopReason != types.StopAborted {
		t.Fatalf("stop: %s", state.StopReason)
	}
}

func TestApproverErrorPropagates(t *testing.T) {
	p := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{{ID: "1", Name: "rm", Args: json.RawMessage(`{}`)}}},
	}}
	a := core.NewAgent(p, core.WithTools(echoTool{}), core.WithHooks(New(func(context.Context, *types.ToolCall) (bool, error) {
		return false, assertErr("ui closed")
	})))
	_, err := a.Run(context.Background(), "hi")
	if err == nil || err.Error() != "ui closed" {
		t.Fatalf("want approver error, got %v", err)
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
