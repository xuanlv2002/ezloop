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
	"github.com/xuanlv2002/ezloop/types"
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

// 免卸载名单：名单内工具的大结果原样保留。
func TestOffloadSkip(t *testing.T) {
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "dump", `{}`)),
			testutil.Text("done"),
		),
		core.WithTools(bigTool{n: 10_000}),
		core.WithHooks(New(fs.NewLocal(t.TempDir()), WithSkip("dump"))),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if msg := state.Messages[2].Content; msg != strings.Repeat("x", 10_000) {
		t.Fatalf("skipped tool must keep full output, got %d bytes", len(msg))
	}
}

// 配置回放工具：摘要尾部提示模型用它回放全文；该工具自身的结果免卸载。
func TestReplayTool(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys, WithReplayTool("read_file"))
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "dump", `{}`)),
			testutil.Text("done"),
		),
		core.WithTools(bigTool{n: 10_000}),
		core.WithHooks(hook),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// 摘要尾部提示回放工具（read_file），卸载路径就在同一条消息里。
	msg := state.Messages[2].Content
	if !strings.Contains(msg, "已卸载到 .ezloop/offload/") ||
		!strings.Contains(msg, "可使用 tool read_file 进行全量内容回放") {
		t.Fatalf("summary must mention offload path and replay tool: %q", msg)
	}

	// 回放工具自身的大结果免卸载：全文进上下文才是回放的意义。
	result := &types.ToolResult{Name: "read_file", Content: strings.Repeat("x", 10_000)}
	if herr := hook.OnToolEnd(context.Background(), state, result); herr != nil {
		t.Fatalf("err: %v", herr)
	}
	if result.Content != strings.Repeat("x", 10_000) {
		t.Fatal("replay tool output must be whitelisted from offload")
	}
}
