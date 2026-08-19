/*
Package filetools 提供四个原子工具：read_file / write_file / edit_file /
terminal，通过 StartHook 注入，依赖 fs.FileSystem 最小接口。

目录浏览与内容搜索不单独做工具——terminal 直接承担（dir/ls、
grep/findstr），避免工具层重复实现。写类操作经 per-path 修改队列
串行化，防止并发工具执行时对同一文件的写冲突。终端执行有真实
副作用，生产组装建议配 approve 审批。
*/
package filetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

type Hook struct {
	fsys fs.FileSystem

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-path 修改队列
}

/* New 创建文件与终端工具 hook。 */
func New(fsys fs.FileSystem) *Hook {
	return &Hook{fsys: fsys, locks: make(map[string]*sync.Mutex)}
}

func (h *Hook) Name() string { return "filetools" }

func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(readFileTool{fsys: h.fsys})
	state.Tools.Register(writeFileTool{h: h})
	state.Tools.Register(editFileTool{h: h})
	state.Tools.Register(terminalTool{})
	injectOSHint(state)
	return nil
}

/*
injectOSHint 把当前系统信息拼进 system（单条 system 政策，与 skill 同
拼接模式）：terminal 工具执行于原生 shell，模型据此书写对应语法。
GOOS 进程内恒定，注入内容每轮一致，不影响前缀缓存。
*/
func injectOSHint(state *types.LoopState) {
	hint := "# 系统环境\n当前操作系统：" + runtime.GOOS +
		"（terminal 工具用系统原生 shell 执行，Windows 请写 cmd 语法 dir/type/findstr，" +
		"类 Unix 请写 POSIX 语法 ls/cat/grep）"
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content += "\n\n" + hint
		return
	}
	state.Messages = append([]types.Message{{
		Role:    types.RoleSystem,
		Content: hint,
	}}, state.Messages...)
}

/* lockPath 返回路径级修改队列锁：同一文件的修改串行，不同文件并行。 */
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

// ---- read_file ----

const readMaxBytes = 50 << 10 // 50KB，防超大文件撑爆上下文

type readFileTool struct{ fsys fs.FileSystem }

func (readFileTool) Name() string        { return "read_file" }
func (readFileTool) Description() string { return "读取文件内容（50KB 截断）" }
func (readFileTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"path":{"type":"string","description":"文件路径"}
	},"required":["path"]}`)
}

func (t readFileTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Path == "" {
		return "", errors.New("path is required")
	}
	data, err := t.fsys.Read(ctx, in.Path)
	if err != nil {
		return "", err
	}
	if len(data) > readMaxBytes {
		return string(data[:readMaxBytes]) + "\n[已达 50KB 上限，已截断]", nil
	}
	return string(data), nil
}

// ---- write_file ----

type writeFileTool struct{ h *Hook }

func (writeFileTool) Name() string        { return "write_file" }
func (writeFileTool) Description() string { return "写入文件（覆盖），自动创建目录" }
func (writeFileTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"path":{"type":"string"},"content":{"type":"string"}
	},"required":["path","content"]}`)
}

func (t writeFileTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Path == "" {
		return "", errors.New("path is required")
	}
	unlock := t.h.lockPath(in.Path)
	defer unlock()
	if err := t.h.fsys.Write(ctx, in.Path, []byte(in.Content)); err != nil {
		return "", err
	}
	return fmt.Sprintf("written %d bytes to %s", len(in.Content), in.Path), nil
}

// ---- edit_file ----

type editFileTool struct{ h *Hook }

func (editFileTool) Name() string { return "edit_file" }
func (editFileTool) Description() string {
	return "对现有文件做查找替换（全部命中，原子）：old_text 必须存在于文件中"
}
func (editFileTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}
	},"required":["path","old_text","new_text"]}`)
}

func (t editFileTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Path    string `json:"path"`
		OldText string `json:"old_text"`
		NewText string `json:"new_text"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Path == "" || in.OldText == "" {
		return "", errors.New("path and old_text are required")
	}
	unlock := t.h.lockPath(in.Path)
	defer unlock()
	n, err := t.h.fsys.Edit(ctx, in.Path, in.OldText, in.NewText)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("replaced %d occurrence(s) in %s", n, in.Path), nil
}

// ---- terminal（EnableExec）----

/*
terminalTool 在系统原生终端执行单条命令：Windows 是 cmd，类 Unix 是 sh。
不做 shell 探测与切换——设计立场是"模型适配环境"：工具描述与 system
注入都标明当前系统，模型据此书写对应语法（Windows 写 dir/type/findstr，
类 Unix 写 ls/cat/grep）。
输出为 stdout+stderr 合并；退出码非零时输出与退出码一并作为正常结果
返回（不报工具错误）——真实报错信息是模型自纠的依据，仅执行失败
（无法启动/取消）才是 error。
*/
type terminalTool struct{}

func (terminalTool) Name() string { return "terminal" }
func (terminalTool) Description() string {
	if runtime.GOOS == "windows" {
		return "在系统终端执行命令（当前系统 Windows，cmd 语法：dir、type、findstr、&、&&、|）；" +
			"非零退出码时输出与错误码一并返回，据此修正命令"
	}
	return "在系统终端执行命令（当前系统 " + runtime.GOOS + "，POSIX sh 语法：ls、cat、grep、管道与 &&）；" +
		"非零退出码时输出与错误码一并返回，据此修正命令"
}
func (terminalTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
		"command":{"type":"string","description":"完整终端命令，语法须与当前系统一致"}
	},"required":["command"]}`)
}

func (terminalTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &in); err != nil || strings.TrimSpace(in.Command) == "" {
		return "", errors.New("command is required")
	}
	name, flag := "sh", "-c"
	if runtime.GOOS == "windows" {
		name, flag = "cmd", "/c"
	}
	out, err := exec.CommandContext(ctx, name, flag, in.Command).CombinedOutput()
	text := strings.TrimSpace(strings.ToValidUTF8(string(out), ""))
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return text + fmt.Sprintf("\n[exit code %d]", exitErr.ExitCode()), nil
	}
	if err != nil {
		return text, fmt.Errorf("run: %w", err)
	}
	return text, nil
}
