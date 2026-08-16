package askuser

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

// 提问 → emit 事件 → 回答作为工具结果进入历史，壳工具 Invoke 不执行。
func TestAskUserAnswerBecomesResult(t *testing.T) {
	h, answers := New()

	var mu sync.Mutex
	var requests []string
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", ToolName, `{"question":"用哪个端口?"}`)),
			testutil.Text("done"),
		),
		core.WithTools(Tool()),
		core.WithHooks(h),
		core.WithOnEvent(func(e event.Event) {
			if e.Type != EventRequest {
				return
			}
			call := e.Data.(*types.ToolCall)
			mu.Lock()
			requests = append(requests, string(call.Args))
			mu.Unlock()
			go func() { answers <- Answer{CallID: call.ID, Input: "8080"} }()
		}),
	).Run(context.Background(), "deploy")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted {
		t.Fatalf("stop: %s", state.StopReason)
	}
	if len(requests) != 1 || !strings.Contains(requests[0], "用哪个端口?") {
		t.Fatalf("requests: %v", requests)
	}
	if state.Messages[2].Content != "8080" {
		t.Fatalf("answer msg: %q", state.Messages[2].Content)
	}
}

// 未挂 Hook 时壳工具报错，装配问题尽早暴露。
func TestToolWithoutHookFails(t *testing.T) {
	if _, err := Tool().Invoke(context.Background(), nil); err == nil {
		t.Fatal("want error when hook not registered")
	}
}
