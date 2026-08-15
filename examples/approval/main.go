// 轮次式人机审批示例：
// 第 1 轮 rm 被拦截 → 模拟用户点击批准（写入 approval store）
// → 第 2 轮携带历史重新 Run → approve hook 查到批准记录放行 → 工具执行。
// 全程无引擎内挂起状态，可恢复状态就是 state.Messages（纯数据，可存库）。
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/hook/approve"
	"github.com/xuanlv2002/ezloop/types"
)

// scriptedProvider 按脚本返回响应，模拟真实模型行为。
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

// rmTool 模拟危险操作。
type rmTool struct{}

func (rmTool) Name() string                { return "rm" }
func (rmTool) Description() string         { return "delete a file" }
func (rmTool) ArgsSchema() json.RawMessage { return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`) }
func (rmTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	var in struct{ Path string }
	_ = json.Unmarshal(args, &in)
	return "deleted " + in.Path, nil
}

func main() {
	ctx := context.Background()

	// 审批记录：approve-once 语义（命中即消费，下次需重新审批）。
	store := approve.NewStore(true)

	rmArgs := json.RawMessage(`{"path":"a.txt"}`)

	round1 := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "需要删除文件", ToolCalls: []types.ToolCall{
			{ID: "c1", Name: "rm", Args: rmArgs},
		}},
		{Content: "删除操作在等待用户审批，我先停在这里。"},
	}}
	round2 := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "用户已批准，重新执行删除", ToolCalls: []types.ToolCall{
			{ID: "c2", Name: "rm", Args: rmArgs},
		}},
		{Content: "已删除 a.txt，任务完成。"},
	}}

	newAgent := func(p *scriptedProvider) *core.Agent {
		return core.NewAgent(p,
			core.WithTools(rmTool{}),
			core.WithHooks(approve.New(store.Approver())),
			core.WithOnEvent(func(e event.Event) {
				if e.Type == event.EventToolEnd {
					fmt.Printf("[event] %s\n", e)
				}
			}),
		)
	}

	// ── 第 1 轮：工具被拦截 ──────────────────────────────
	fmt.Println("═══ 第 1 轮 ═══")
	state1, err := newAgent(round1).Run(ctx, "删除 a.txt")
	if err != nil {
		panic(err)
	}
	fmt.Printf("stop=%s\n", state1.StopReason)
	for _, m := range state1.Messages {
		fmt.Printf("  %-9s %s\n", m.Role, m.Content)
	}

	// ── 模拟 UI：用户点击"批准" ────────────────────────
	fmt.Println("\n═══ 用户点击批准，写入 approval store ═══")
	store.Approve("rm", rmArgs)

	// ── 第 2 轮：携带历史，approve hook 查到记录放行 ────
	fmt.Println("\n═══ 第 2 轮 ═══")
	state2, err := newAgent(round2).Run(ctx, "用户已批准，继续执行", core.WithHistory(state1.Messages...))
	if err != nil {
		panic(err)
	}
	fmt.Printf("stop=%s\n", state2.StopReason)
	for _, m := range state2.Messages {
		fmt.Printf("  %-9s %s\n", m.Role, m.Content)
	}
}
