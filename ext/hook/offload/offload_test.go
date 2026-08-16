package offload

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/internal/testutil"
)

type bigTool struct{ n int }

func (bigTool) Name() string                { return "dump" }
func (bigTool) Description() string         { return "" }
func (bigTool) ArgsSchema() json.RawMessage { return nil }
func (b bigTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return strings.Repeat("x", b.n), nil
}

type failFS struct{ fs.Local }

func (failFS) Write(context.Context, string, []byte) error { return fmt.Errorf("disk full") }

func run(t *testing.T, fsys fs.FileSystem) string {
	t.Helper()
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "dump", `{}`)),
			testutil.Text("done"),
		),
		core.WithTools(bigTool{n: 10_000}),
		core.WithHooks(New(fsys)),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	return state.Messages[2].Content
}

// 大结果卸载到 FS，消息里只留摘要与路径；写入失败降级透传原文。
func TestOffloadAndDegrade(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	msg := run(t, fsys)
	if !strings.Contains(msg, "已卸载到") || !strings.Contains(msg, "10000 字节") {
		t.Fatalf("offload msg: %q", msg)
	}
	entries, err := fsys.List(context.Background(), ".ezloop/offload")
	if err != nil || len(entries) != 1 {
		t.Fatalf("offload dir: %+v %v", entries, err)
	}

	if msg := run(t, failFS{}); msg != strings.Repeat("x", 10_000) {
		t.Fatal("write failure must degrade to passthrough")
	}
}
