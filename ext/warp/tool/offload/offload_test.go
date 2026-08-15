package offload

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

// bigTool 返回指定长度的重复文本。
type bigTool struct{ n int }

func (t bigTool) Name() string                { return "dump" }
func (t bigTool) Description() string         { return "" }
func (t bigTool) ArgsSchema() json.RawMessage { return nil }
func (t bigTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return strings.Repeat("x", t.n), nil
}

// failFS 写入总是失败，验证降级路径。
type failFS struct{ fs.Local }

func (failFS) Write(context.Context, string, []byte) error {
	return fmt.Errorf("disk full")
}

func runWithOffload(t *testing.T, fsys fs.FileSystem, tool types.Tool) *types.LoopState {
	t.Helper()
	p := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{{ID: "1", Name: tool.Name(), Args: json.RawMessage(`{}`)}}},
		{Content: "done"},
	}}
	a := core.NewAgent(p, core.WithTools(tool),
		core.WithToolWarp(Warp(fsys)))
	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	return state
}

func TestOffloadLargeResult(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	state := runWithOffload(t, fsys, bigTool{n: 10_000})

	msg := state.Messages[2]
	if !strings.Contains(msg.Content, "已卸载到") {
		t.Fatalf("tool msg should reference offloaded file: %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "10000 字节") {
		t.Fatalf("should mention total size: %q", msg.Content)
	}

	// 卸载文件内容完整。
	entries, err := fsys.List(context.Background(), ".ezloop/offload")
	if err != nil || len(entries) != 1 {
		t.Fatalf("offload dir: %+v %v", entries, err)
	}
	data, err := fsys.Read(context.Background(), ".ezloop/offload/"+entries[0].Name)
	if err != nil {
		t.Fatalf("offloaded file: %v", err)
	}
	if len(data) != 10_000 {
		t.Fatalf("offloaded size: %d", len(data))
	}
}

func TestOffloadSmallResultPassthrough(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	state := runWithOffload(t, fsys, bigTool{n: 100})
	if state.Messages[2].Content != strings.Repeat("x", 100) {
		t.Fatal("small result should pass through unchanged")
	}
}

func TestOffloadDegradesOnWriteFailure(t *testing.T) {
	state := runWithOffload(t, failFS{}, bigTool{n: 10_000})
	if state.Messages[2].Content != strings.Repeat("x", 10_000) {
		t.Fatal("write failure must degrade to full passthrough")
	}
}

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
