/*
tools.go 定义 filetools 的四个工具，全部经 types.NewTool 构造：
schema 从参数结构体的 tag 反射生成（json 定名，desc 描述，omitempty
定非必填），不再手写 JSON schema。
*/
package filetools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

const readMaxBytes = 50 << 10 // 50KB，防超大文件撑爆上下文

type readArgs struct {
	Path string `json:"path" desc:"文件路径"`
}

func readTool(fsys fs.FileSystem) types.Tool {
	return types.NewTool("read_file", "读取文件内容（50KB 截断）",
		func(ctx context.Context, in *readArgs) (string, error) {
			if in.Path == "" {
				return "", errors.New("path is required")
			}
			data, err := fsys.Read(ctx, in.Path)
			if err != nil {
				return "", err
			}
			if len(data) > readMaxBytes {
				return string(data[:readMaxBytes]) + "\n[已达 50KB 上限，已截断]", nil
			}
			return string(data), nil
		})
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func writeTool(h *Hook) types.Tool {
	return types.NewTool("write_file", "写入文件（覆盖），自动创建目录",
		func(ctx context.Context, in *writeArgs) (string, error) {
			if in.Path == "" {
				return "", errors.New("path is required")
			}
			unlock := h.lockPath(in.Path)
			defer unlock()
			if err := h.fsys.Write(ctx, in.Path, []byte(in.Content)); err != nil {
				return "", err
			}
			return fmt.Sprintf("written %d bytes to %s", len(in.Content), in.Path), nil
		})
}

type editArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"` // 允许空串：查找替换的删除语义
}

func editTool(h *Hook) types.Tool {
	return types.NewTool("edit_file", "对现有文件做查找替换（全部命中，原子）：old_text 必须存在于文件中",
		func(ctx context.Context, in *editArgs) (string, error) {
			if in.Path == "" || in.OldText == "" {
				return "", errors.New("path and old_text are required")
			}
			unlock := h.lockPath(in.Path)
			defer unlock()
			n, err := h.fsys.Edit(ctx, in.Path, in.OldText, in.NewText)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("replaced %d occurrence(s) in %s", n, in.Path), nil
		})
}

type terminalArgs struct {
	Command string `json:"command" desc:"完整终端命令，语法须与当前系统一致"`
}

/*
terminalTool 在系统原生终端执行单条命令：Windows 是 cmd，类 Unix 是 sh。
不做 shell 探测与切换——设计立场是"模型适配环境"：工具描述与 system
注入都标明当前系统，模型据此书写对应语法（Windows 写 dir/type/findstr，
类 Unix 写 ls/cat/grep）。
输出为 stdout+stderr 合并；退出码非零时输出与退出码一并作为正常结果
返回（不报工具错误）——真实报错信息是模型自纠的依据，仅执行失败
（无法启动/取消）才是 error。
*/
func terminalTool() types.Tool {
	return types.NewTool("terminal", terminalDesc(),
		func(ctx context.Context, in *terminalArgs) (string, error) {
			if strings.TrimSpace(in.Command) == "" {
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
		})
}

/* terminalDesc 按当前系统给模型提示对应 shell 语法；GOOS 进程内恒定。 */
func terminalDesc() string {
	if runtime.GOOS == "windows" {
		return "在系统终端执行命令（当前系统 Windows，cmd 语法：dir、type、findstr、&、&&、|）；" +
			"非零退出码时输出与错误码一并返回，据此修正命令"
	}
	return "在系统终端执行命令（当前系统 " + runtime.GOOS + "，POSIX sh 语法：ls、cat、grep、管道与 &&）；" +
		"非零退出码时输出与错误码一并返回，据此修正命令"
}
