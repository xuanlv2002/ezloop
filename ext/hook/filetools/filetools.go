// Package filetools 提供基于 FileSystem 接口的核心文件工具集与终端执行工具，
// 通过 StartHook 注入，工具能力由传入文件系统实现的能力接口决定：
//
//	FileSystem（必须） read_file（分页）/ write_file / list_dir
//	Modifier（可选）   edit_file（原子查找替换）/ apply_patch（多文件原子修改）
//	Searcher（可选）   grep（正则搜内容）/ find（glob 查文件名）
//	EnableExec 选项    bash（shell 执行，默认关闭，建议配 approve）
//
// 所有修改类操作（write/edit/patch）经 per-path 修改队列串行化，
// 防止并发工具执行时对同一文件的写冲突。
package filetools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

type Options struct {
	// EnableExec 启用 bash 终端执行工具，默认关闭。
	EnableExec bool
}

type Hook struct {
	fsys fs.FileSystem
	opts Options

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-path 修改队列
}

func New(fsys fs.FileSystem, opts ...func(*Options)) *Hook {
	h := &Hook{fsys: fsys, locks: make(map[string]*sync.Mutex)}
	for _, fn := range opts {
		fn(&h.opts)
	}
	return h
}

func (h *Hook) Name() string { return "filetools" }

func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(readFileTool{fsys: h.fsys})
	state.Tools.Register(writeFileTool{h: h})
	state.Tools.Register(listDirTool{fsys: h.fsys})

	// 能力探测：实现了哪个接口，注册哪组工具。
	if m, ok := h.fsys.(fs.Modifier); ok {
		state.Tools.Register(editFileTool{h: h, mod: m})
		state.Tools.Register(applyPatchTool{h: h, mod: m})
	}
	if s, ok := h.fsys.(fs.Searcher); ok {
		state.Tools.Register(grepTool{s: s})
		state.Tools.Register(findTool{s: s})
	}
	if h.opts.EnableExec {
		state.Tools.Register(bashTool{})
	}
	return nil
}

// lockPath 返回路径级修改队列锁：同一文件的修改串行，不同文件并行。
func (h *Hook) lockPath(path string) func() {
	h.mu.Lock()
	l, ok := h.locks[path]
	if !ok {
		l = &sync.Mutex{}
		h.locks[path] = l
	}
	h.mu.Unlock()
	l.Lock()
	return l.Unlock
}

// ---- read_file：行分页 + 字节上限，防上下文溢出 ----

const (
	readDefaultLines = 2000
	readMaxBytes     = 50 << 10 // 50KB
)

type readFileTool struct{ fsys fs.FileSystem }

func (readFileTool) Name() string        { return "read_file" }
func (readFileTool) Description() string { return "读取文本文件，支持 offset/limit 行分页（默认前 2000 行，50KB 上限）" }
func (readFileTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"path":{"type":"string","description":"文件路径"},
		"offset":{"type":"integer","description":"起始行（0 起，默认 0）"},
		"limit":{"type":"integer","description":"行数（默认 2000）"}
	},"required":["path"]}`)
}

func (t readFileTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset int    `json:"offset"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	if in.Limit <= 0 {
		in.Limit = readDefaultLines
	}
	data, err := t.fsys.Read(ctx, in.Path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")
	total := len(lines)
	if in.Offset >= total {
		return fmt.Sprintf("[offset=%d 超出总行数 %d]", in.Offset, total), nil
	}
	end := in.Offset + in.Limit
	if end > total {
		end = total
	}
	page := strings.Join(lines[in.Offset:end], "\n")
	if len(page) > readMaxBytes {
		page = page[:readMaxBytes] + fmt.Sprintf("\n[已达 50KB 上限，剩余内容请用 offset 继续分页读取]")
	}
	if end < total {
		page += fmt.Sprintf("\n[显示 %d-%d 行，共 %d 行，继续读取请设 offset=%d]", in.Offset+1, end, total, end)
	}
	return page, nil
}

// ---- write_file ----

type writeFileTool struct{ h *Hook }

func (writeFileTool) Name() string        { return "write_file" }
func (writeFileTool) Description() string { return "写入文件（覆盖），自动创建目录" }
func (writeFileTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}

func (t writeFileTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct{ Path, Content string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if in.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	unlock := t.h.lockPath(in.Path)
	defer unlock()
	if err := t.h.fsys.Write(ctx, in.Path, []byte(in.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("written %d bytes to %s", len(in.Content), in.Path), nil
}

// ---- edit_file（Modifier）----

type editFileTool struct {
	h   *Hook
	mod fs.Modifier
}

func (editFileTool) Name() string        { return "edit_file" }
func (editFileTool) Description() string { return "对现有文件做查找替换（原子）：old_text 必须存在于文件中" }
func (editFileTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}},"required":["path","old_text","new_text"]}`)
}

func (t editFileTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	unlock := t.h.lockPath(in.Path)
	defer unlock()
	n, err := t.mod.Edit(ctx, in.Path, in.OldText, in.NewText)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", n, in.Path), nil
}

// ---- apply_patch（Modifier，多文件原子修改）----

type applyPatchTool struct {
	h   *Hook
	mod fs.Modifier
}

func (applyPatchTool) Name() string        { return "apply_patch" }
func (applyPatchTool) Description() string { return "一次原子修改多个文件：每个操作为查找替换，任一失败全部不生效" }
func (applyPatchTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"ops":{"type":"array","items":{"type":"object","properties":{
			"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}
		},"required":["path","old_text","new_text"]}}
	},"required":["ops"]}`)
}

func (t applyPatchTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Ops []fs.PatchOp `json:"ops"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if len(in.Ops) == 0 {
		return "", fmt.Errorf("ops is empty")
	}
	// 同一批补丁内可能含同文件多 op：按路径加锁后整体应用。
	paths := make([]string, 0, len(in.Ops))
	seen := map[string]bool{}
	for _, op := range in.Ops {
		if !seen[op.Path] {
			seen[op.Path] = true
			paths = append(paths, op.Path)
		}
	}
	sort.Strings(paths)
	unlocks := make([]func(), 0, len(paths))
	for _, p := range paths {
		unlocks = append(unlocks, t.h.lockPath(p))
	}
	defer func() {
		for i := len(unlocks) - 1; i >= 0; i-- {
			unlocks[i]()
		}
	}()
	if err := t.mod.ApplyPatch(ctx, in.Ops); err != nil {
		return "", err
	}
	return fmt.Sprintf("patched %d file(s)", len(in.Ops)), nil
}

// ---- list_dir ----

type listDirTool struct{ fsys fs.FileSystem }

func (listDirTool) Name() string        { return "list_dir" }
func (listDirTool) Description() string { return "列出目录内容" }
func (listDirTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"目录路径，默认 ."}}}`)
}

func (t listDirTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct{ Path string }
	_ = json.Unmarshal(args, &in)
	if in.Path == "" {
		in.Path = "."
	}
	entries, err := t.fsys.List(ctx, in.Path)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		tag := "file"
		if e.IsDir {
			tag = "dir "
		}
		lines = append(lines, fmt.Sprintf("%s %8d %s", tag, e.Size, e.Name))
	}
	if len(lines) == 0 {
		return "(empty directory)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// ---- grep（Searcher）----

type grepTool struct{ s fs.Searcher }

func (grepTool) Name() string        { return "grep" }
func (grepTool) Description() string { return "按正则表达式搜索文件内容" }
func (grepTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"pattern":{"type":"string","description":"正则表达式"},
		"path":{"type":"string","description":"搜索起点，默认 ."},
		"glob":{"type":"string","description":"文件名过滤，如 *.go"}
	},"required":["pattern"]}`)
}

func (t grepTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct{ Pattern, Path, Glob string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if in.Path == "" {
		in.Path = "."
	}
	matches, err := t.s.Grep(ctx, fs.GrepRequest{Pattern: in.Pattern, Path: in.Path, Glob: in.Glob})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "no matches", nil
	}
	lines := make([]string, 0, len(matches))
	for _, m := range matches {
		lines = append(lines, fmt.Sprintf("%s:%d: %s", m.Path, m.Line, m.Text))
	}
	return strings.Join(lines, "\n"), nil
}

// ---- find（Searcher）----

type findTool struct{ s fs.Searcher }

func (findTool) Name() string        { return "find" }
func (findTool) Description() string { return "按文件名 glob 模式查找文件" }
func (findTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"pattern":{"type":"string","description":"文件名 glob，如 *.go"},
		"root":{"type":"string","description":"查找根目录，默认 ."}
	},"required":["pattern"]}`)
}

func (t findTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct{ Pattern, Root string }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if in.Root == "" {
		in.Root = "."
	}
	found, err := t.s.Find(ctx, in.Root, in.Pattern)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "no matches", nil
	}
	return strings.Join(found, "\n"), nil
}

// ---- bash（EnableExec）----

// bashTool 以 shell 语义执行单条命令字符串（Windows: cmd /c，其他: sh -c），
// 支持管道、重定向与 && 组合。输出为 stdout+stderr 合并；退出码非零时
// 返回输出与错误（供模型自纠）。
type bashTool struct{}

func (bashTool) Name() string        { return "bash" }
func (bashTool) Description() string { return "执行 shell 命令并返回合并输出（支持管道、重定向、&& 组合）" }
func (bashTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"command":{"type":"string","description":"完整 shell 命令，如 ls -la | grep go"}
	},"required":["command"]}`)
}

func (bashTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct{ Command string `json:"command"` }
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(in.Command) == "" {
		return "", fmt.Errorf("command is required")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", in.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", in.Command)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("exit: %w", err)
	}
	return string(out), nil
}
