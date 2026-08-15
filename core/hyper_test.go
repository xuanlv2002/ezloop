package core

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
)

// sleepTool 记录同时在飞的调用数。
type sleepTool struct {
	mu       sync.Mutex
	inflight int
	maxSeen  int
	delay    time.Duration
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

func calls(n int) []types.ToolCall {
	out := make([]types.ToolCall, n)
	for i := range out {
		out[i] = types.ToolCall{ID: fmt.Sprintf("c%d", i), Name: "sleep", Args: json.RawMessage(`{}`)}
	}
	return out
}

func TestHyperParamsDefaults(t *testing.T) {
	a := NewAgent(&mockProvider{})
	if a.hyper.MaxIterations != DefaultMaxIterations || a.hyper.MaxConcurrency != 1 {
		t.Fatalf("defaults: %+v", a.hyper)
	}
	b := NewAgent(&mockProvider{}, WithHyperParams(HyperParams{MaxConcurrency: 4}))
	if b.hyper.MaxIterations != DefaultMaxIterations || b.hyper.MaxConcurrency != 4 {
		t.Fatalf("partial defaults: %+v", b.hyper)
	}
}

func TestConcurrentToolExecution(t *testing.T) {
	tool := &sleepTool{delay: 50 * time.Millisecond}
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: calls(4)},
		textResp("all done"),
	}}
	a := NewAgent(p,
		WithTools(tool),
		WithHyperParams(HyperParams{MaxConcurrency: 4, MaxIterations: 4}),
	)

	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if tool.maxSeen != 4 {
		t.Fatalf("want 4 concurrent invocations, max seen %d", tool.maxSeen)
	}
	// 消息保序：tool 消息按 call 顺序。
	for i, want := range []string{"c0", "c1", "c2", "c3"} {
		msg := state.Messages[2+i]
		if msg.ToolCallID != want {
			t.Fatalf("msg[%d] call=%s want %s", i, msg.ToolCallID, want)
		}
	}
}

func TestSerialByDefault(t *testing.T) {
	tool := &sleepTool{delay: 10 * time.Millisecond}
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: calls(3)},
		textResp("done"),
	}}
	a := NewAgent(p, WithTools(tool))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if tool.maxSeen != 1 {
		t.Fatalf("default must be serial, max seen %d", tool.maxSeen)
	}
}

// 并发中工具 panic 不崩进程，转为错误结果。
type panicConcurrentTool struct{}

func (panicConcurrentTool) Name() string                { return "boom" }
func (panicConcurrentTool) Description() string         { return "" }
func (panicConcurrentTool) ArgsSchema() json.RawMessage { return nil }
func (panicConcurrentTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	panic("concurrent crash")
}

func TestConcurrentToolPanicRecovered(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{
			{ID: "a", Name: "boom", Args: json.RawMessage(`{}`)},
			{ID: "b", Name: "boom", Args: json.RawMessage(`{}`)},
		}},
		textResp("survived"),
	}}
	a := NewAgent(p, WithTools(panicConcurrentTool{}), WithHyperParams(HyperParams{MaxConcurrency: 2}))
	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	for _, m := range state.Messages {
		if m.Role == types.RoleTool && m.Err == "" {
			t.Fatal("panic tool should produce error result")
		}
	}
}

func TestContextCancellation(t *testing.T) {
	block := &blockingProvider{release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	a := NewAgent(block)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	state, err := a.Run(ctx, "hi")
	if err == nil {
		t.Fatal("want ctx error")
	}
	if state.StopReason != types.StopCancelled {
		t.Fatalf("stop: %s", state.StopReason)
	}
}

type blockingProvider struct{ release chan struct{} }

func (p *blockingProvider) Invoke(ctx context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.release:
		return &types.ModelResponse{Content: "ok"}, nil
	}
}

func TestRunAsync(t *testing.T) {
	p := &mockProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{{ID: "1", Name: "echo", Args: json.RawMessage(`{}`)}}},
		textResp("finished"),
	}}
	a := NewAgent(p, WithTools(echoTool{}))

	h := a.RunAsync(context.Background(), "hi")
	var types_ []event.EventType
	for e := range h.Events() {
		types_ = append(types_, e.Type)
	}
	state, err := h.Wait()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if len(types_) == 0 || types_[0] != event.EventLoopStart || types_[len(types_)-1] != event.EventLoopEnd {
		t.Fatalf("events: %v", types_)
	}
}

func TestRunAsyncCancel(t *testing.T) {
	block := &blockingProvider{release: make(chan struct{})}
	a := NewAgent(block)

	h := a.RunAsync(context.Background(), "hi")
	go func() {
		time.Sleep(50 * time.Millisecond)
		h.Cancel()
	}()
	state, err := h.Wait()
	if err == nil {
		t.Fatal("want cancel error")
	}
	if state.StopReason != types.StopCancelled {
		t.Fatalf("stop: %s", state.StopReason)
	}
}

