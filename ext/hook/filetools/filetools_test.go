package filetools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/ext/fs"
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

func runToolCall(t *testing.T, hook *Hook, name, args string) string {
	t.Helper()
	p := &scriptedProvider{script: []*types.ModelResponse{
		{Content: "", ToolCalls: []types.ToolCall{
			{ID: "1", Name: name, Args: json.RawMessage(args)},
		}},
		{Content: "done"},
	}}
	a := core.NewAgent(p, core.WithHooks(hook))
	state, err := a.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	msg := state.Messages[2]
	if msg.Err != "" {
		t.Fatalf("tool %s error: %s", name, msg.Err)
	}
	return msg.Content
}

func TestReadFilePagination(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i)
	}
	_ = fsys.Write(context.Background(), "big.txt", []byte(strings.Join(lines, "\n")))
	hook := New(fsys)

	first := runToolCall(t, hook, "read_file", `{"path":"big.txt","limit":10}`)
	if !strings.Contains(first, "line 0") || !strings.Contains(first, "line 9") ||
		!strings.Contains(first, "[显示 1-10 行，共 100 行") {
		t.Fatalf("page 1: %q", first)
	}
	second := runToolCall(t, hook, "read_file", `{"path":"big.txt","offset":10,"limit":10}`)
	if !strings.Contains(second, "line 10") || strings.Contains(second, "line 9\n") {
		t.Fatalf("page 2: %q", second)
	}
}

func TestWriteEditPatchGrepFind(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys) // fs.Local 实现了 Modifier + Searcher

	if out := runToolCall(t, hook, "write_file", `{"path":"app.go","content":"package main\n// TODO fix"}`); !strings.Contains(out, "written") {
		t.Fatalf("write: %q", out)
	}
	if out := runToolCall(t, hook, "edit_file", `{"path":"app.go","old_text":"TODO fix","new_text":"DONE"}`); !strings.Contains(out, "replaced 1") {
		t.Fatalf("edit: %q", out)
	}
	_ = runToolCall(t, hook, "write_file", `{"path":"b.go","content":"package b"}`)
	if out := runToolCall(t, hook, "apply_patch", `{"ops":[
		{"path":"app.go","old_text":"DONE","new_text":"SHIPPED"},
		{"path":"b.go","old_text":"package b","new_text":"package beta"}
	]}`); !strings.Contains(out, "patched 2") {
		t.Fatalf("patch: %q", out)
	}
	if out := runToolCall(t, hook, "grep", `{"pattern":"SHIPPED"}`); !strings.Contains(out, "app.go:2") {
		t.Fatalf("grep: %q", out)
	}
	if out := runToolCall(t, hook, "find", `{"pattern":"*.go"}`); !strings.Contains(out, "app.go") || !strings.Contains(out, "b.go") {
		t.Fatalf("find: %q", out)
	}
}

// bareFS 只暴露核心接口方法：edit/grep/find/patch 不应被注册。
type bareFS struct{ inner fs.FileSystem }

func (b bareFS) Read(ctx context.Context, p string) ([]byte, error) { return b.inner.Read(ctx, p) }
func (b bareFS) Write(ctx context.Context, p string, d []byte) error {
	return b.inner.Write(ctx, p, d)
}
func (b bareFS) List(ctx context.Context, dir string) ([]fs.Entry, error) {
	return b.inner.List(ctx, dir)
}

func TestCapabilityDetection(t *testing.T) {
	a := core.NewAgent(&scriptedProvider{script: []*types.ModelResponse{{Content: "x"}}},
		core.WithHooks(New(bareFS{inner: fs.NewLocal(t.TempDir())})))
	state, _ := a.Run(context.Background(), "hi")
	for _, name := range []string{"edit_file", "apply_patch", "grep", "find", "run_command"} {
		if _, err := state.Tools.Lookup(name); err == nil {
			t.Fatalf("%s must not be registered on bare FileSystem", name)
		}
	}
	for _, name := range []string{"read_file", "write_file", "list_dir"} {
		if _, err := state.Tools.Lookup(name); err != nil {
			t.Fatalf("%s must be registered: %v", name, err)
		}
	}
}

// 并发写同一文件必须被修改队列串行化：最终内容是某一次写入的完整结果，
// 绝不能是交错残缺。
func TestMutationQueueSerializesSamePath(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys)

	const writers = 8
	var wg sync.WaitGroup
	var clean atomic.Int64
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			content := strings.Repeat(fmt.Sprintf("w%d", n), 1000)
			_, err := writeFileTool{h: hook}.Invoke(context.Background(), mustJSON(t, map[string]string{
				"path": "same.txt", "content": content,
			}))
			if err == nil {
				clean.Add(1)
			}
		}(i)
	}
	wg.Wait()

	data, err := fsys.Read(context.Background(), "same.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(data)
	// 完整写入的内容是某个写入者的完整串：1000 次 "wN" 共 2000 字节。
	if len(s) != 2000 || !strings.HasPrefix(s, "w") {
		t.Fatalf("corrupted write (%d bytes): %.40s", len(s), s)
	}
	// 内容只含一种写入者的标记。
	first := s[:2]
	if strings.Count(s, first) != 1000 {
		t.Fatalf("interleaved writes detected: %s", first)
	}
	
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
