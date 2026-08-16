package filetools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/internal/testutil"
)

func runTool(t *testing.T, hook *Hook, name, args string) string {
	t.Helper()
	state, err := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", name, args)),
			testutil.Text("done"),
		),
		core.WithHooks(hook),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	msg := state.Messages[2]
	if msg.Err != "" {
		t.Fatalf("tool %s: %s", name, msg.Err)
	}
	return msg.Content
}

// 核心链路：分页读取 + 全套文件操作。
func TestFileToolsCore(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys)

	lines := make([]string, 20)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	_ = fsys.Write(context.Background(), "big.txt", []byte(strings.Join(lines, "\n")))

	page := runTool(t, hook, "read_file", `{"path":"big.txt","limit":5}`)
	if !strings.Contains(page, "line 4") || !strings.Contains(page, "[显示 1-5 行，共 20 行") {
		t.Fatalf("pagination: %q", page)
	}

	if out := runTool(t, hook, "write_file", `{"path":"app.go","content":"package main // TODO"}`); !strings.Contains(out, "written") {
		t.Fatalf("write: %q", out)
	}
	if out := runTool(t, hook, "edit_file", `{"path":"app.go","old_text":"TODO","new_text":"DONE"}`); !strings.Contains(out, "replaced 1") {
		t.Fatalf("edit: %q", out)
	}
	if out := runTool(t, hook, "grep", `{"pattern":"DONE"}`); !strings.Contains(out, "app.go:1") {
		t.Fatalf("grep: %q", out)
	}
	if out := runTool(t, hook, "find", `{"pattern":"*.go"}`); !strings.Contains(out, "app.go") {
		t.Fatalf("find: %q", out)
	}
}

// 能力探测：bare FileSystem 不解锁 edit/grep/find/patch/exec。
type bareFS struct{ inner fs.FileSystem }

func (b bareFS) Read(ctx context.Context, p string) ([]byte, error) { return b.inner.Read(ctx, p) }
func (b bareFS) Write(ctx context.Context, p string, d []byte) error {
	return b.inner.Write(ctx, p, d)
}
func (b bareFS) List(ctx context.Context, dir string) ([]fs.Entry, error) {
	return b.inner.List(ctx, dir)
}

func TestCapabilityDetection(t *testing.T) {
	state, err := core.NewAgent(
		testutil.Scripted(testutil.Text("x")),
		core.WithHooks(New(bareFS{inner: fs.NewLocal(t.TempDir())})),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, name := range []string{"edit_file", "apply_patch", "grep", "find", "bash"} {
		if _, lerr := state.Tools.Lookup(name); lerr == nil {
			t.Fatalf("%s must not register on bare FileSystem", name)
		}
	}
	if _, lerr := state.Tools.Lookup("read_file"); lerr != nil {
		t.Fatalf("read_file must register: %v", lerr)
	}
}

// bash：shell 语义执行（Windows: cmd /c，Unix: sh -c），退出码非零报错。
func TestBashTool(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys, func(o *Options) { o.EnableExec = true })

	out := runTool(t, hook, "bash", `{"command":"echo ezloop-bash"}`)
	if !strings.Contains(out, "ezloop-bash") {
		t.Fatalf("bash output: %q", out)
	}

	// 失败命令：输出 + 错误回传模型自纠。
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", "bash", `{"command":"definitely_missing_cmd_xyz"}`)),
		testutil.Text("done"),
	)
	state, err := core.NewAgent(p, core.WithHooks(hook)).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.Messages[2].Err == "" {
		t.Fatal("failed command should produce tool error")
	}
}

// 修改队列：并发写同一路径，最终内容必须是某次完整写入。
func TestMutationQueue(t *testing.T) {
	hook := New(fs.NewLocal(t.TempDir()))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			args, _ := json.Marshal(map[string]string{
				"path": "same.txt", "content": strings.Repeat(fmt.Sprintf("w%d", n), 500),
			})
			_, _ = writeFileTool{h: hook}.Invoke(context.Background(), args)
		}(i)
	}
	wg.Wait()
	data, err := hook.fsys.Read(context.Background(), "same.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(data)
	if len(s) != 1000 || strings.Count(s, s[:2]) != 500 {
		t.Fatal("concurrent writes interleaved — mutation queue failed")
	}
}
