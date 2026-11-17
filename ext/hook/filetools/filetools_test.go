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
		`{"path":"app.go","edits":[{"old_text":"TODO","new_text":"DONE"}]}`); !strings.Contains(out, "#1 第 1 行：替换 1 处") {
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

	// 窗口截取：第 3 行起 4 行；尾部统一给总行数、本次区间与续读 offset。
	out := runTool(t, hook, "read_file", `{"path":"ten.txt","offset":3,"limit":4}`)
	for _, want := range []string{"line3", "line6", `total_lines="10"`, `read="3-6"`, `next_offset="7"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("window read missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "line2") || strings.Contains(out, "line7") {
		t.Fatalf("window read leaked outside lines: %q", out)
	}

	// 行号按需开启：前缀「行号+Tab」，默认读取不带。
	out = runTool(t, hook, "read_file", `{"path":"ten.txt","offset":3,"limit":2,"line_numbers":true}`)
	if !strings.Contains(out, "3\tline3") || !strings.Contains(out, "4\tline4") || strings.Contains(out, "5\tline5") {
		t.Fatalf("line numbers: %q", out)
	}
	if plain := runTool(t, hook, "read_file", `{"path":"ten.txt","offset":3,"limit":2}`); strings.Contains(plain, "\t") {
		t.Fatalf("default read must not carry line numbers: %q", plain)
	}

	// 末页：窗口越过文件尾，读到结尾且无续读提示。
	out = runTool(t, hook, "read_file", `{"path":"ten.txt","offset":8,"limit":100}`)
	if !strings.Contains(out, "line10") || !strings.Contains(out, `read="8-10"`) || strings.Contains(out, "next_offset") {
		t.Fatalf("last page: %q", out)
	}

	// 整文件读完同样带元信息：区间覆盖全文件，无续读 offset、无 warning。
	out = runTool(t, hook, "read_file", `{"path":"ten.txt"}`)
	if !strings.Contains(out, `total_lines="10"`) || !strings.Contains(out, `read="1-10"`) ||
		strings.Contains(out, "next_offset") || strings.Contains(out, "warning=") {
		t.Fatalf("whole read: %q", out)
	}

	// offset 越界：警告也收进元信息标签，带「!」。
	if out = runTool(t, hook, "read_file", `{"path":"ten.txt","offset":99}`); !strings.Contains(out, `warning="! offset 99 超出文件总行数 10"`) || !strings.Contains(out, `read=""`) {
		t.Fatalf("out of range: %q", out)
	}

	// 空文件：只回元信息，total_lines=0。
	if err := hook.fsys.Write(context.Background(), "empty.txt", nil); err != nil {
		t.Fatal(err)
	}
	if out = runTool(t, hook, "read_file", `{"path":"empty.txt"}`); out != `<read_file_meta total_lines="0" read=""/>` {
		t.Fatalf("empty file: %q", out)
	}

	// 超长单行：按字符截断；没有一行完整返回，区间留空。
	long := []byte(strings.Repeat("x", readMaxChars+10))
	if err := hook.fsys.Write(context.Background(), "long.txt", long); err != nil {
		t.Fatal(err)
	}
	if out = runTool(t, hook, "read_file", `{"path":"long.txt"}`); !strings.Contains(out, `total_lines="1"`) ||
		!strings.Contains(out, `read=""`) || !strings.Contains(out, `warning="! 本次内容共`) {
		t.Fatalf("char truncation: %q", out[max(0, len(out)-200):])
	}
}

// 五件套恒注册：read_file / write_file / edit_file / grep / terminal。
func TestRegistersAllTools(t *testing.T) {
	state, err := core.NewAgent(
		testutil.Scripted(testutil.Text("x")),
		core.WithHooks(New(fs.NewLocal(t.TempDir()))),
	).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, name := range []string{"read_file", "write_file", "edit_file", "grep", "terminal"} {
		if _, lerr := state.Tools.Lookup(name); lerr != nil {
			t.Fatalf("%s must register: %v", name, lerr)
		}
	}
	// schema 会直接进模型请求：非法 JSON 会让每次请求在本地 marshal 阶段失败。
	for _, tool := range state.Tools.List() {
		if s := tool.ArgsSchema(); len(s) > 0 && !json.Valid(s) {
			t.Errorf("%s ArgsSchema 非法 JSON：%s", tool.Name(), s)
		}
	}
}

// edit_file：一次提交多处精确修改；整批原子；多处命中默认拒绝。
func TestEditBatch(t *testing.T) {
	hook := New(fs.NewLocal(t.TempDir()))
	writeFile := func(path, content string) {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"path": path, "content": content})
		runTool(t, hook, "write_file", string(args))
	}
	readFile := func(path string) string {
		t.Helper()
		data, err := hook.fsys.Read(context.Background(), path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(data)
	}

	writeFile("a.go", "func a() {\n\tvalue := 1\n\treturn value\n}\n")
	out := runTool(t, hook, "edit_file", `{"path":"a.go","edits":[
		{"old_text":"func a() {","new_text":"func b() {"},
		{"old_text":"\tvalue := 1\n\treturn value","new_text":"\tvalue := 2\n\treturn value"}]}`)
	if !strings.Contains(out, "#1 第 1 行：替换 1 处") || !strings.Contains(out, "#2 第 2-3 行：替换 1 处") {
		t.Fatalf("batch edit: %q", out)
	}
	if got := readFile("a.go"); !strings.Contains(got, "func b()") ||
		!strings.Contains(got, "\tvalue := 2") || !strings.Contains(got, "\treturn value") {
		t.Fatalf("edited content: %q", got)
	}

	// 精确匹配的多行原文：返回起止行号。
	if out := runTool(t, hook, "edit_file",
		`{"path":"a.go","edits":[{"old_text":"func b() {\n\tvalue := 2","new_text":"func b() {\n\tvalue := 3"}]}`); !strings.Contains(out, "#1 第 1-2 行：替换 1 处") {
		t.Fatalf("multi-line exact label: %q", out)
	}

	// 整批原子：第二处失败时第一处也不落盘。
	before := readFile("a.go")
	result, err := editTool(hook).Invoke(context.Background(), json.RawMessage(
		`{"path":"a.go","edits":[{"old_text":"func b()","new_text":"func c()"},{"old_text":"missing","new_text":"x"}]}`))
	if err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("missing old_text must fail with re-read hint, got %q / %v", result, err)
	}
	if after := readFile("a.go"); after != before {
		t.Fatalf("failed batch must not write: %q", after)
	}

	// 同一 old_text 多处命中：默认拒绝；显式 replace_all 才全部替换并返回行号。
	writeFile("multi.txt", "x\nkeep\nx\n")
	_, err = editTool(hook).Invoke(context.Background(), json.RawMessage(
		`{"path":"multi.txt","edits":[{"old_text":"x","new_text":"y"}]}`))
	if err == nil || !strings.Contains(err.Error(), "replace_all") {
		t.Fatalf("multiple exact matches must fail: %v", err)
	}
	if out := runTool(t, hook, "edit_file",
		`{"path":"multi.txt","edits":[{"old_text":"x","new_text":"y","replace_all":true}]}`); !strings.Contains(out, "#1 第 1、3 行：替换 2 处") {
		t.Fatalf("multi occurrence label: %q", out)
	}

	// CRLF 文件：按 LF 写的 old_text 能命中，替换后整文件仍是 CRLF。
	writeFile("crlf.txt", "a\r\n\tvalue := 1\r\nb\r\n")
	if out := runTool(t, hook, "edit_file",
		`{"path":"crlf.txt","edits":[{"old_text":"\tvalue := 1","new_text":"\tvalue := 2"}]}`); !strings.Contains(out, "#1 第 2 行：替换 1 处") {
		t.Fatalf("crlf edit: %q", out)
	}
	if got := readFile("crlf.txt"); got != "a\r\n\tvalue := 2\r\nb\r\n" {
		t.Fatalf("crlf not preserved: %q", got)
	}

	// 缩进/空白不一致不再模糊命中：报错让模型重新读原文。
	writeFile("exact.txt", "\tif ok {\n\t\trun()\n\t}\n")
	_, err = editTool(hook).Invoke(context.Background(), json.RawMessage(
		`{"path":"exact.txt","edits":[{"old_text":"if ok {\n\trun()\n}","new_text":"if ok {\n\trun2()\n}"}]}`))
	if err == nil || !strings.Contains(err.Error(), "read_file") {
		t.Fatalf("whitespace mismatch must fail: %v", err)
	}
	if got := readFile("exact.txt"); got != "\tif ok {\n\t\trun()\n\t}\n" {
		t.Fatalf("failed edit must not write: %q", got)
	}
}

// edit schema：edits 与元素内 old_text/new_text 必填，replace_all 可选。
func TestEditSchema(t *testing.T) {
	schema := string(editTool(New(fs.NewLocal(t.TempDir()))).ArgsSchema())
	if !strings.Contains(schema, `"edits":{"type":"array"`) ||
		!strings.Contains(schema, `"required":["old_text","new_text"]`) ||
		!strings.Contains(schema, `"required":["path","edits"]`) {
		t.Fatalf("edit schema: %s", schema)
	}
}

// grep：递归命中带行号；跳过依赖目录与二进制；include 过滤文件名。
func TestGrepTool(t *testing.T) {
	hook := New(fs.NewLocal(t.TempDir()))
	writeFile := func(path, content string) {
		t.Helper()
		args, _ := json.Marshal(map[string]string{"path": path, "content": content})
		runTool(t, hook, "write_file", string(args))
	}
	writeFile("src/app.ts", "const x = useDomainList()\n")
	writeFile("src/deep/b.ts", "// useDomainList\n")
	writeFile("src/c.md", "useDomainList\n")
	writeFile("node_modules/pkg/index.js", "useDomainList\n")

	out := runTool(t, hook, "grep", `{"pattern":"useDomainList"}`)
	if !strings.Contains(out, "src/app.ts:1:") || !strings.Contains(out, "src/deep/b.ts:1:") ||
		!strings.Contains(out, "命中 3 处 / 3 个文件") {
		t.Fatalf("grep output: %q", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("grep must skip node_modules: %q", out)
	}

	out = runTool(t, hook, "grep", `{"pattern":"useDomainList","include":"*.ts"}`)
	if strings.Contains(out, "c.md") || !strings.Contains(out, "命中 2 处 / 2 个文件") {
		t.Fatalf("include filter: %q", out)
	}

	out = runTool(t, hook, "grep", `{"pattern":"useDomainList","fixed_string":true,"max_results":1}`)
	if !strings.Contains(out, "已达 1 条上限") {
		t.Fatalf("max results: %q", out)
	}

	// 非 UTF-8 文件跳过并在摘要里报数：避免把「没搜到」当成「不存在」。
	if err := hook.fsys.Write(context.Background(), "gbk.txt", []byte{0xC4, 0xE3, 0xBA, 0xC3}); err != nil {
		t.Fatal(err)
	}
	out = runTool(t, hook, "grep", `{"pattern":"useDomainList"}`)
	if !strings.Contains(out, "跳过 1 个非 UTF-8") {
		t.Fatalf("skip summary: %q", out)
	}

	// CRLF 文件按去掉 \r 后的行匹配：$ 锚点仍有效。
	writeFile("crlf.txt", "useDomainList\r\n")
	out = runTool(t, hook, "grep", `{"pattern":"useDomainList$","path":"crlf.txt"}`)
	if !strings.Contains(out, "crlf.txt:1:") {
		t.Fatalf("crlf match: %q", out)
	}

	// 相邻命中共享上下文时，命中行仍标 ":"，不降级成上下文 "-"。
	writeFile("adj.txt", "a\nhit\nhit\nb\n")
	out = runTool(t, hook, "grep", `{"pattern":"hit","path":"adj.txt","context_lines":1}`)
	if !strings.Contains(out, "adj.txt:2: hit") || !strings.Contains(out, "adj.txt:3: hit") ||
		strings.Contains(out, "--") {
		t.Fatalf("adjacent hits: %q", out)
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

// 挂起命令按 timeout_s 杀树终止：已产出输出 + 超时标记作为正常结果返回
// （不是工具错误，模型据此换 term_* 或加时重跑）。
func TestTerminalTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns a real shell")
	}
	fsys := fs.NewLocal(t.TempDir())
	hook := New(fsys)
	hang := "sleep 30"
	if runtime.GOOS == "windows" {
		hang = "ping -n 30 127.0.0.1 > nul"
	}
	p := testutil.Scripted(
		testutil.ToolCalls(testutil.Call("1", "terminal", `{"command":"`+hang+`","timeout_s":1}`)),
		testutil.Text("done"),
	)
	state, err := core.NewAgent(p, core.WithHooks(hook)).Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	for _, m := range state.Messages {
		if m.Role != types.RoleTool {
			continue
		}
		if m.Err != "" {
			t.Fatalf("timeout must not be tool error: %q", m.Err)
		}
		if !strings.Contains(m.Content, "[超时") {
			t.Fatalf("timeout marker missing: %q", m.Content)
		}
		return
	}
	t.Fatal("no tool result message")
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
