package filetools

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
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
	for _, m := range state.Messages {
		if m.Role == types.RoleTool {
			if m.Err != "" {
				t.Fatalf("tool %s: %s", name, m.Err)
			}
			return m.Content
		}
	}
	t.Fatal("no tool result message")
	return ""
}

// 核心链路：write → read → edit。
func TestFileToolsCore(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys)

	if out := runTool(t, hook, "write_file",
		`{"path":"app.go","content":"package main // TODO"}`); !strings.Contains(out, "written") {
		t.Fatalf("write: %q", out)
	}
	if out := runTool(t, hook, "read_file", `{"path":"app.go"}`); !strings.Contains(out, "TODO") {
		t.Fatalf("read: %q", out)
	}
	if out := runTool(t, hook, "edit_file",
		`{"path":"app.go","old_text":"TODO","new_text":"DONE"}`); !strings.Contains(out, "replaced 1") {
		t.Fatalf("edit: %q", out)
	}
	if out := runTool(t, hook, "read_file", `{"path":"app.go"}`); !strings.Contains(out, "DONE") {
		t.Fatalf("after edit: %q", out)
	}
}

// read_file 分页：offset/limit 取行窗口，尾部标注剩余行数与续读 offset。
func TestReadFilePaging(t *testing.T) {
	hook := New(fs.NewLocal(t.TempDir()))
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line%d\n", i)
	}
	writeArgs, _ := json.Marshal(map[string]string{"path": "ten.txt", "content": b.String()})
	if out := runTool(t, hook, "write_file", string(writeArgs)); !strings.Contains(out, "written") {
		t.Fatalf("write: %q", out)
	}

	// 窗口截取：第 3 行起 4 行。
	out := runTool(t, hook, "read_file", `{"path":"ten.txt","offset":3,"limit":4}`)
	for _, want := range []string{"line3", "line6", "已读第 3-6 行，共 10 行", "offset=7"} {
		if !strings.Contains(out, want) {
			t.Fatalf("window read missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "line2") || strings.Contains(out, "line7") {
		t.Fatalf("window read leaked outside lines: %q", out)
	}

	// 末页：窗口越过文件尾，读到结尾且无续读提示。
	out = runTool(t, hook, "read_file", `{"path":"ten.txt","offset":8,"limit":100}`)
	if !strings.Contains(out, "line10") || strings.Contains(out, "offset=") {
		t.Fatalf("last page: %q", out)
	}

	// offset 越界：明确告知总行数而非报错。
	if out = runTool(t, hook, "read_file", `{"path":"ten.txt","offset":99}`); !strings.Contains(out, "总行数 10") {
		t.Fatalf("out of range: %q", out)
	}
}

// 四件套恒注册：read_file / write_file / edit_file / terminal。
func TestRegistersAllTools(t *testing.T) {
	state, err := core.NewAgent(
		testutil.Scripted(testutil.Text("x")),
		core.WithHooks(New(fs.NewLocal(t.TempDir()))),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, name := range []string{"read_file", "write_file", "edit_file", "terminal"} {
		if _, lerr := state.Tools.Lookup(name); lerr != nil {
			t.Fatalf("%s must register: %v", name, lerr)
		}
	}
}

// terminal：系统原生 shell 执行（Windows: cmd，类 Unix: sh）；
// 退出码非零时输出与退出码作为正常结果回传模型自纠。
func TestTerminalTool(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys)

	// injectOSHint 会插入 system 消息，按 role 取 tool 结果而非硬索引。
	run := func(command string) types.Message {
		t.Helper()
		p := testutil.Scripted(
			testutil.ToolCalls(testutil.Call("1", "terminal", `{"command":"`+command+`"}`)),
			testutil.Text("done"),
		)
		state, err := core.NewAgent(p, core.WithHooks(hook)).Run(context.Background(), "hi")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		// 系统信息注入断言：单条 system 打头，含 GOOS。
		if state.Messages[0].Role != types.RoleSystem ||
			!strings.Contains(state.Messages[0].Content, "当前操作系统："+runtime.GOOS) {
			t.Fatalf("os hint not injected: %+v", state.Messages[0])
		}
		for _, m := range state.Messages {
			if m.Role == types.RoleTool {
				return m
			}
		}
		t.Fatal("no tool result message")
		return types.Message{}
	}

	if out := run("echo ezloop-terminal"); !strings.Contains(out.Content, "ezloop-terminal") || out.Err != "" {
		t.Fatalf("terminal output: %+v", out)
	}

	// 各系统原生语法可用。
	native := "ls > /dev/null && echo ok"
	failing := "echo boom 1>&2; exit 3"
	if runtime.GOOS == "windows" {
		native = "ver > NUL & echo ok"
		failing = "echo boom 1>&2 & exit 3"
	}
	if out := run(native); !strings.Contains(out.Content, "ok") || out.Err != "" {
		t.Fatalf("native syntax output: %+v", out)
	}

	// 失败命令：输出 + [exit code N] 一并作为正常结果（不是工具错误）。
	out := run(failing)
	if out.Err != "" {
		t.Fatalf("non-zero exit must not be tool error: %q", out.Err)
	}
	if !strings.Contains(out.Content, "boom") || !strings.Contains(out.Content, "[exit code 3]") {
		t.Fatalf("output should contain stderr and exit code: %q", out.Content)
	}
}

// 引号参数原样执行：Windows 上 EscapeArg 的 \" 转义 cmd 不认，带引号
// 的参数（findstr /C:"…"、含空格路径）曾整条损坏——回归护栏。
func TestTerminalQuotedArgs(t *testing.T) {
	dir := t.TempDir()
	hook := New(fs.NewLocal(dir), WithWorkDir(dir))
	runTool(t, hook, "write_file", `{"path":"q.txt","content":"hello world"}`)

	cmd := `grep "hello world" q.txt`
	if runtime.GOOS == "windows" {
		cmd = `findstr /C:"hello world" q.txt`
	}
	args, _ := json.Marshal(map[string]string{"command": cmd})
	if out := runTool(t, hook, "terminal", string(args)); !strings.Contains(out, "hello world") {
		t.Fatalf("quoted args output: %q", out)
	}
}

// 修改队列：并发写同一路径，最终内容必须是某次完整写入。
func TestMutationQueue(t *testing.T) {
	hook := New(fs.NewLocal(t.TempDir()))
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			args, _ := json.Marshal(map[string]string{
				"path": "same.txt", "content": strings.Repeat(fmt.Sprintf("w%d", n), 500),
			})
			_, _ = writeTool(hook).Invoke(context.Background(), args)
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

// GBK 输出（Windows 控制台 OEM 代码页）解码为 UTF-8；UTF-8 原文透传。
func TestDecodeOutput(t *testing.T) {
	if got := decodeOutput([]byte("plain ascii")); got != "plain ascii" {
		t.Fatalf("ascii passthrough: %q", got)
	}
	msg := "文件名、目录名或卷标语法不正确。"
	if got := decodeOutput([]byte(msg)); got != msg { // 已是 UTF-8：原样
		t.Fatalf("utf8 passthrough: %q", got)
	}
	gbk, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(msg))
	if err != nil {
		t.Fatalf("encode gbk: %v", err)
	}
	if got := decodeOutput(gbk); got != msg {
		t.Fatalf("gbk decode: %q", got)
	}
}

// chcp 65001 重定向输出带 BOM，解码时剥掉。
func TestDecodeOutputBOM(t *testing.T) {
	if got := decodeOutput([]byte{0xEF, 0xBB, 0xBF, 'h', 'i'}); got != "hi" {
		t.Fatalf("bom strip: %q", got)
	}
}
