package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// mockProvider 按脚本顺序返回响应。
type mockProvider struct {
	script []*types.ModelResponse
	calls  int
}

func (p *mockProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	if p.calls >= len(p.script) {
		return &types.ModelResponse{}, nil
	}
	resp := p.script[p.calls]
	p.calls++
	return resp, nil
}

func textResp(s string) *types.ModelResponse {
	return &types.ModelResponse{Content: s}
}

func toolResp(calls ...types.ToolCall) *types.ModelResponse {
	return &types.ModelResponse{Content: "calling tools", ToolCalls: calls}
}

// echoTool 回显参数。
type echoTool struct{}

func (echoTool) Name() string              { return "echo" }
func (echoTool) Description() string       { return "echo args" }
func (echoTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"msg":{"type":"string"}}}`)
}
func (echoTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	return "echo: " + string(args), nil
}

func collectEvents(t *testing.T, a *Agent) *[]event.Event {
	t.Helper()
	var evs []event.Event
	a.onEvent = func(e event.Event) { evs = append(evs, e) }
	return &evs
}

func eventTypes(evs []event.Event) []event.EventType {
	out := make([]event.EventType, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.Type)
	}
	return out
}

func TestRunCompletesWithoutTools(t *testing.T) {
	a := NewAgent(&mockProvider{script: []*types.ModelResponse{textResp("hello")}})
	evs := collectEvents(t, a)

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("want completed, got %s", state.StopReason)
	}
	if state.LastResponse.Content != "hello" {
		t.Fatalf("bad content: %s", state.LastResponse.Content)
	}
	want := []event.EventType{event.EventLoopStart, event.EventModelStart, event.EventModelEnd, event.EventLoopEnd}
	if fmt.Sprint(eventTypes(*evs)) != fmt.Sprint(want) {
		t.Fatalf("events: %v", eventTypes(*evs))
	}
}

func TestRunLoopbackWithTools(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		toolResp(types.ToolCall{ID: "1", Name: "echo", Args: json.RawMessage(`{"msg":"a"}`)}),
		textResp("done after tool"),
	}}
	a := NewAgent(p, WithTools(echoTool{}))
	evs := collectEvents(t, a)

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("want completed, got %s", state.StopReason)
	}
	if state.Iteration != 2 {
		t.Fatalf("want 2 iterations, got %d", state.Iteration)
	}
	// 消息序列: user, assistant(toolcall), tool, assistant
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
	if fmt.Sprint(eventTypes(*evs)) != fmt.Sprint(wantEv) {
		t.Fatalf("events: %v", eventTypes(*evs))
	}
}

func TestRunMaxIterations(t *testing.T) {
	// 模型永远请求工具。
	call := types.ToolCall{ID: "1", Name: "echo", Args: json.RawMessage(`{}`)}
	script := make([]*types.ModelResponse, 10)
	for i := range script {
		script[i] = toolResp(call)
	}
	a := NewAgent(&mockProvider{script: script}, WithTools(echoTool{}), WithMaxIterations(3))

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopMaxIteration {
		t.Fatalf("want max_iterations, got %s", state.StopReason)
	}
	if state.Iteration != 3 {
		t.Fatalf("want 3 iterations, got %d", state.Iteration)
	}
}

// denyHook 拦截指定工具。
type denyHook struct {
	name   string
	action hook.Action
}

func (h denyHook) Name() string { return "deny" }
func (h denyHook) OnToolStart(_ context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name == h.name {
		return h.action, nil
	}
	return hook.ActionProceed, nil
}

func TestRunToolStartHookSkip(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		toolResp(types.ToolCall{ID: "1", Name: "echo", Args: json.RawMessage(`{}`)}),
		textResp("ok"),
	}}
	a := NewAgent(p, WithTools(echoTool{}), WithHooks(denyHook{name: "echo", action: hook.ActionSkip}))

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 跳过后循环继续，第二轮完成。
	if state.StopReason != types.StopCompleted {
		t.Fatalf("want completed, got %s", state.StopReason)
	}
	toolMsg := state.Messages[2]
	if !strings.Contains(toolMsg.Content, "skipped") {
		t.Fatalf("tool msg should be skipped, got %q", toolMsg.Content)
	}
}

func TestRunToolStartHookAbort(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		toolResp(types.ToolCall{ID: "1", Name: "echo", Args: json.RawMessage(`{}`)}),
	}}
	a := NewAgent(p, WithTools(echoTool{}), WithHooks(denyHook{name: "echo", action: hook.ActionAbort}))

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopAborted {
		t.Fatalf("want aborted, got %s", state.StopReason)
	}
}

// failHook 在指定阶段返回错误。
type failHook struct {
	phase   string
	endRan  *bool
}

func (h failHook) Name() string { return "fail" }

func (h failHook) OnModelStart(_ context.Context, _ *types.LoopState) error {
	if h.phase == "modelStart" {
		return fmt.Errorf("boom")
	}
	return nil
}

func (h failHook) OnEnd(_ context.Context, _ *types.LoopState) error {
	if h.endRan != nil {
		*h.endRan = true
	}
	return nil
}

func TestRunHookErrorStopsButEndHookRuns(t *testing.T) {
	endRan := false
	a := NewAgent(
		&mockProvider{script: []*types.ModelResponse{textResp("x")}},
		WithHooks(failHook{phase: "modelStart", endRan: &endRan}),
	)
	evs := collectEvents(t, a)

	state, err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("want error")
	}
	if state.StopReason != types.StopError {
		t.Fatalf("want error stop, got %s", state.StopReason)
	}
	if !endRan {
		t.Fatal("end hook must run on failure")
	}
	hasErrEvent := false
	for _, e := range *evs {
		if e.Type == event.EventError {
			hasErrEvent = true
		}
	}
	if !hasErrEvent {
		t.Fatal("want error event")
	}
}

// stopHook 在首轮模型响应后置 Stop。
type stopHook struct{}

func (stopHook) Name() string { return "stop" }
func (stopHook) OnModelEnd(_ context.Context, s *types.LoopState) error {
	s.Stop = true
	return nil
}

func TestRunStopFromModelEndHook(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		toolResp(types.ToolCall{ID: "1", Name: "echo", Args: json.RawMessage(`{}`)}),
	}}
	a := NewAgent(p, WithTools(echoTool{}), WithHooks(stopHook{}))

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopAborted {
		t.Fatalf("want aborted, got %s", state.StopReason)
	}
	if state.Iteration != 1 {
		t.Fatalf("want 1 iteration, got %d", state.Iteration)
	}
}

// streamProvider 逐 chunk 输出。
type streamProvider struct{ mockProvider }

func (p *streamProvider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	resp, err := p.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, r := range []rune(resp.Content) {
		if err := onChunk(types.ModelChunk{ContentDelta: string(r)}); err != nil {
			return nil, err
		}
	}
	return resp, nil
}

func TestRunStreaming(t *testing.T) {
	p := &streamProvider{}
	p.script = []*types.ModelResponse{textResp("abc")}
	a := NewAgent(p, WithStreaming(true))
	evs := collectEvents(t, a)

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("want completed, got %s", state.StopReason)
	}
	chunks := 0
	for _, e := range *evs {
		if e.Type == event.EventModelChunk {
			chunks++
		}
	}
	if chunks != 3 {
		t.Fatalf("want 3 chunks, got %d", chunks)
	}
}
