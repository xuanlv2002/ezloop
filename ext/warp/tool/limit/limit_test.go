package limit

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// slowTool 记录在途调用的并发峰值。
type slowTool struct {
	mu       sync.Mutex
	inFlight int
	peak     int
}

func (s *slowTool) Name() string                { return "slow" }
func (s *slowTool) Description() string         { return "slow tool" }
func (s *slowTool) ArgsSchema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (s *slowTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	s.mu.Lock()
	s.inFlight++
	if s.inFlight > s.peak {
		s.peak = s.inFlight
	}
	s.mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	s.inFlight--
	s.mu.Unlock()
	return "ok", nil
}

// 引擎一轮 fan-out 6 个调用,闸为 2 时在途峰值不得超过 2
// (不限流的基线峰值是 6)。
func TestLimitCapsFanOut(t *testing.T) {
	calls := make([]types.ToolCall, 6)
	for i := range calls {
		calls[i] = testutil.Call(fmtID(i), "slow", `{}`)
	}
	tool := &slowTool{}
	a := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(calls...),
			testutil.Text("done"),
		),
		core.WithToolWarp(Warp(2)),
		core.WithTools(tool),
	)
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.peak < 1 || tool.peak > 2 {
		t.Fatalf("peak concurrency: want 1..2, got %d", tool.peak)
	}
}

// n<1 视为 1:全部调用串行通过。
func TestLimitMinOne(t *testing.T) {
	calls := make([]types.ToolCall, 3)
	for i := range calls {
		calls[i] = testutil.Call(fmtID(i), "slow", `{}`)
	}
	tool := &slowTool{}
	a := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(calls...),
			testutil.Text("done"),
		),
		core.WithToolWarp(Warp(0)),
		core.WithTools(tool),
	)
	if _, err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("run: %v", err)
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if tool.peak != 1 {
		t.Fatalf("peak: want 1, got %d", tool.peak)
	}
}

func fmtID(i int) string {
	return "c" + string(rune('0'+i))
}
