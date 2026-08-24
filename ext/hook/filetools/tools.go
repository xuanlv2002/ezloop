/*
tools.go 定义 filetools 的四个工具，全部经 types.NewTool 构造：
schema 从参数结构体的 tag 反射生成（json 定名，desc 描述，omitempty
定非必填），不再手写 JSON schema。
*/
package filetools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

const readDefaultLines = 2000 // 默认读取行数，防大文件一次撑爆上下文

type readArgs struct {
	Path   string `json:"path" desc:"文件路径"`
	Offset int    `json:"offset,omitempty" desc:"起始行号，1 起，默认 1"`
	Limit  int    `json:"limit,omitempty" desc:"读取行数，默认 2000"`
}

/*
readTool 按行分页读取：offset/limit 缺省时读前 2000 行；未读完时尾部
标注剩余行数与续读 offset，模型据此翻页——大文件对模型不再是只有
前半截的黑盒。
*/
func readTool(fsys fs.FileSystem) types.Tool {
	return types.NewTool("read_file", "按行读取文件内容（默认第 1 行起 2000 行，可指定 offset/limit 翻页）",
		func(ctx context.Context, in *readArgs) (string, error) {
			if in.Path == "" {
				return "", errors.New("path is required")
			}
			if in.Offset <= 0 {
				in.Offset = 1
			}
			if in.Limit <= 0 {
				in.Limit = readDefaultLines
			}
			data, err := fsys.Read(ctx, in.Path)
			if err != nil {
				return "", err
			}
			lines := splitLines(string(data))
			if len(lines) == 0 {
				return "", nil
			}
			if in.Offset > len(lines) {
				return fmt.Sprintf("offset %d 超出文件总行数 %d", in.Offset, len(lines)), nil
			}
			end := min(in.Offset-1+in.Limit, len(lines))
			out := strings.Join(lines[in.Offset-1:end], "\n")
			if end < len(lines) {
				out += fmt.Sprintf("\n[已读第 %d-%d 行，共 %d 行；继续读取请设 offset=%d]",
					in.Offset, end, len(lines), end+1)
			}
			return out, nil
		})
}

/* splitLines 按 \n 切行并剥掉 \r；结尾换行不产生末尾空行。 */
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
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
workDir 为执行目录（空 = 进程 cwd）——宿主可把终端锚定到用户工作目录，
同时提示模型用绝对路径，命令不再依赖进程 cwd。
Windows 下命令前预置 chcp 65001（控制台与重定向文件转 UTF-8；审批按
模型提交的原始命令判定，不受此包装影响），仍按 OEM 代码页输出的老
程序由 decodeOutput 兜底解码。不做 shell 探测与切换——设计立场是
"模型适配环境"：工具描述与 system 注入都标明当前系统，模型据此书写
对应语法（Windows 写 dir/type/findstr，类 Unix 写 ls/cat/grep）。
输出为 stdout+stderr 合并；退出码非零时输出与退出码一并作为正常结果
返回（不报工具错误）——真实报错信息是模型自纠的依据，仅执行失败
（无法启动/取消）才是 error。
*/
func terminalTool(workDir string) types.Tool {
	return types.NewTool("terminal", terminalDesc(),
		func(ctx context.Context, in *terminalArgs) (string, error) {
			if strings.TrimSpace(in.Command) == "" {
				return "", errors.New("command is required")
			}
			name, flag, cmdStr := "sh", "-c", in.Command
			if runtime.GOOS == "windows" {
				name, flag = "cmd", "/c"
				cmdStr = "chcp 65001 >nul & " + in.Command
			}
			cmd := exec.CommandContext(ctx, name, flag, cmdStr)
			cmd.Dir = workDir
			out, err := cmd.CombinedOutput()
			text := strings.TrimSpace(decodeOutput(out))
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

/* decodeOutput 把命令输出归一为 UTF-8：Windows 控制台程序可能按 OEM
代码页（简中 GBK）输出，非 UTF-8 字节按 GB18030（GBK 超集）解码，
失败再剥离无效字节（原文输出不可强求）。chcp 65001 重定向会带 BOM，一并剥掉。 */
func decodeOutput(b []byte) string {
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	if utf8.Valid(b) {
		return string(b)
	}
	if out, err := simplifiedchinese.GB18030.NewDecoder().Bytes(b); err == nil {
		return string(out)
	}
	return strings.ToValidUTF8(string(b), "")
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
