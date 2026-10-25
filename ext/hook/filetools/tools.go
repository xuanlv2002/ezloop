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
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
)

/* EventToolOutput 是 terminal 执行中的实时输出事件（Data 为 ToolOutputData）。 */
const EventToolOutput = event.EventType("tool.output")

/* ToolOutputData 是 tool.output 的载荷：本次调用 + 增量文本。 */
type ToolOutputData struct {
	CallID string `json:"callId"`
	Name   string `json:"name"`
	Text   string `json:"text"`
}

/* termLiveInterval 是实时输出的最小事件间隔：长任务高频输出不塞满事件队列。 */
const termLiveInterval = 150 * time.Millisecond

const readDefaultLines = 2000     // 默认读取行数，防大文件一次撑爆上下文
const readMaxChars = 200_000      // 单次结果字符上限：分页限行不限字节，单行超长（minified）文件需另行设防
const readMaxImageBytes = 8 << 20 // 图片字节上限：超出不进上下文（多模态请求体积防线）

type readArgs struct {
	Path   string `json:"path" desc:"文件路径"`
	Offset int    `json:"offset,omitempty" desc:"起始行号，1 起，默认 1"`
	Limit  int    `json:"limit,omitempty" desc:"读取行数，默认 2000"`
}

/*
readTool 按行分页读取：offset/limit 缺省时读前 2000 行；未读完时尾部
标注剩余行数与续读 offset，模型据此翻页——大文件对模型不再是只有
前半截的黑盒。行数之外另有整段字符上限（readMaxChars）：少数超长行
文件（压缩 JS、单行大 JSON）行数不多但体量巨大，按字符截断兜底。
图片文件走魔数判定的独立分支（文本分页对图片是乱码）：默认返回
imageLoadedMark 标记，由本 hook 的 OnLoop 在回边转换为持久化的
user 图片消息（全协议经 user+Images 通道携带）；装配 WithImageHandler
时由回调决定（loadAsImage=false 返回宿主文案，如无视觉引导）。
*/
func readTool(h *Hook) types.Tool {
	fsys := h.fsys
	return types.NewTool("read_file", "按行读取文件内容（默认第 1 行起 2000 行，可指定 offset/limit 翻页；单次最多返回 200000 字符，超出截断；图片文件作为图片消息进入上下文，不返回文本；二进制文件（pdf/ppt/压缩包/可执行等）暂不支持读取）",
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
			if mime := imageMime(data); mime != "" {
				if len(data) > readMaxImageBytes {
					return fmt.Sprintf("[图片 %s 大小 %d 字节，超出 %d 字节上限，无法加载进上下文；请改用文件路径引用或外部识别工具]",
						in.Path, len(data), readMaxImageBytes), nil
				}
				if h.onImage != nil {
					if text, load := h.onImage(in.Path, mime); !load {
						return text, nil
					}
				}
				return imageLoadedMark(in.Path), nil
			}
			// 二进制兜底：乱码干爆上下文，直接拒读
			if kind, bin := binaryKind(in.Path, data); bin {
				return fmt.Sprintf("read_file 当前暂不支持读取 %s 类型文件（%s）", kind, in.Path), nil
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
			if runes := utf8.RuneCountInString(out); runes > readMaxChars {
				return string([]rune(out)[:readMaxChars]) +
					fmt.Sprintf("\n[本次内容共 %d 字符，超出单次 %d 字符上限已截断；如需其余部分请分批次使用读取]", runes, readMaxChars), nil
			}
			if end < len(lines) {
				out += fmt.Sprintf("\n[已读第 %d-%d 行，共 %d 行；继续读取请设 offset=%d]",
					in.Offset, end, len(lines), end+1)
			}
			return out, nil
		})
}

/* imageLoadedTag 是图片消息的包裹标签名：user 消息形如
<image_loaded>\n路径…\n</image_loaded>，Images 携带 base64（前端按
它识别渲染缩略图行）。工具结果内的机器标记是自闭合变体（见下）。 */
const imageLoadedTag = "image_loaded"

/* imageLoadedMark 是工具结果内的机器标记：OnLoop 识别并转换为
user 图片消息（只在"结果入史→回边"窗口存在，历史里保留原样）。 */
func imageLoadedMark(path string) string {
	return fmt.Sprintf(`<%s path=%q/>`, imageLoadedTag, path)
}

/* imageMarkRe 解析标记里的路径。 */
var imageMarkRe = regexp.MustCompile(`<image_loaded path="([^"]*)"/>`)

/* imageMime 按魔数判定常见图片格式（覆盖多模态 API 的主流接受集）。 */
func imageMime(b []byte) string {
	switch {
	case len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) > 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) > 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
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
	Command  string `json:"command" desc:"完整终端命令，语法须与当前系统一致"`
	TimeoutS int    `json:"timeout_s" desc:"总超时秒数（默认 600，上限 3600）。超时杀进程树，已产出输出与超时标记照常返回"`
}

/*
terminalTool 在系统原生终端执行单条命令：Windows 是 cmd，类 Unix 是 sh。
workDir 为执行目录（空 = 进程 cwd）——宿主可把终端锚定到用户工作目录，
同时提示模型用绝对路径，命令不再依赖进程 cwd。
Windows 下命令前预置 chcp 65001（控制台与重定向文件转 UTF-8；审批按
模型提交的原始命令判定，不受此包装影响），且整条命令行经
SysProcAttr.CmdLine 原样传入（exec.Args 的 EscapeArg 会把模型命令里
的双引号写成 \"，cmd 不认反斜杠转义，带引号参数会被解析坏，见
terminalcmd_windows.go）。仍按 OEM 代码页输出的老程序由 decodeOutput
兜底解码。不做 shell 探测与切换——设计立场是
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
			timeout := clampTimeoutS(in.TimeoutS)
			tctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			cmd := terminalCmd(tctx, in.Command, workDir)
			// 边跑边发 tool.output（宿主实时展示），全量字节仍留给最终结果
			stream := newStreamWriter(ctx)
			cmd.Stdout, cmd.Stderr = stream, stream
			err := cmd.Run()
			stream.close()
			out := stream.bytes()
			text := strings.TrimSpace(decodeOutput(out))
			if errors.Is(tctx.Err(), context.DeadlineExceeded) {
				// 超时与退出码同权：作为正常结果返回（已产出输出 + 标记），
				// 模型据此改用 term_* 或调大 timeout_s 重跑，不误判命令失败
				return text + fmt.Sprintf("\n[超时 %s 后终止：命令未跑完；长驻/交互命令改用 term_* 系工具，或调大 timeout_s 重试]", timeout), nil
			}
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

/* clampTimeoutS 归一总超时：0/负 = 默认 10 分钟，上限 1 小时。 */
func clampTimeoutS(s int) time.Duration {
	if s <= 0 {
		s = 600
	}
	if s > 3600 {
		s = 3600
	}
	return time.Duration(s) * time.Second
}

/*
streamWriter 接管 terminal 的 stdout/stderr：全量字节留给最终结果，同时
每 termLiveInterval 把新增文本作为 tool.output 发给宿主（长构建不再像
卡死）。stdout/stderr 由不同 goroutine 写，加锁；截断在多字节字符中间
的尾部留在 pending 等下一批，避免实时区闪出乱码。
*/
type streamWriter struct {
	ctx     context.Context
	mu      sync.Mutex
	all     bytes.Buffer
	pending []byte
	done    chan struct{}
}

func newStreamWriter(ctx context.Context) *streamWriter {
	w := &streamWriter{ctx: ctx, done: make(chan struct{})}
	go w.tick()
	return w
}

func (w *streamWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.all.Write(p)
	w.pending = append(w.pending, p...)
	return len(p), nil
}

/* tick 定时冲刷 pending：没有换行的进度条/慢输出也能持续可见。 */
func (w *streamWriter) tick() {
	t := time.NewTicker(termLiveInterval)
	defer t.Stop()
	for {
		select {
		case <-w.done:
			return
		case <-t.C:
			w.mu.Lock()
			w.flushLocked()
			w.mu.Unlock()
		}
	}
}

func (w *streamWriter) flushLocked() {
	if len(w.pending) == 0 {
		return
	}
	prefix, rest := splitCompleteUTF8(w.pending)
	if len(prefix) == 0 {
		return
	}
	w.pending = append([]byte(nil), rest...)
	text := decodeOutput(prefix)
	if strings.TrimSpace(text) == "" {
		return
	}
	inv := types.ToolInvocationFromContext(w.ctx)
	if inv == nil || inv.Call == nil {
		return // 无宿主事件出口（直接 Invoke/测试）：只留全量结果
	}
	types.EmitEvent(w.ctx, EventToolOutput, ToolOutputData{CallID: inv.Call.ID, Name: "terminal", Text: text})
}

/* splitCompleteUTF8 切出完整 UTF-8 前缀；尾部若停在多字节字符中间则留下。 */
func splitCompleteUTF8(b []byte) ([]byte, []byte) {
	if utf8.Valid(b) {
		return b, nil
	}
	for i := 1; i <= 3 && i < len(b); i++ {
		if utf8.Valid(b[:len(b)-i]) {
			return b[:len(b)-i], b[len(b)-i:]
		}
	}
	return b, nil
}

/* close 停止定时器并冲刷残余。 */
func (w *streamWriter) close() {
	close(w.done)
	w.mu.Lock()
	w.flushLocked()
	w.mu.Unlock()
}

func (w *streamWriter) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.all.Bytes()...)
}

/*
	decodeOutput 把命令输出归一为 UTF-8：Windows 控制台程序可能按 OEM

代码页（简中 GBK）输出，非 UTF-8 字节按 GB18030（GBK 超集）解码，
失败再剥离无效字节（原文输出不可强求）。chcp 65001 重定向会带 BOM，一并剥掉。
*/
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
			"非零退出码时输出与错误码一并返回，据此修正命令。总超时默认 10 分钟（timeout_s 可调，上限 1 小时），" +
			"超时终止并带回已产出输出；长驻/交互命令用 term_* 系工具"
	}
	return "在系统终端执行命令（当前系统 " + runtime.GOOS + "，POSIX sh 语法：ls、cat、grep、管道与 &&）；" +
		"非零退出码时输出与错误码一并返回，据此修正命令。总超时默认 10 分钟（timeout_s 可调，上限 1 小时），" +
		"超时终止并带回已产出输出；长驻/交互命令用 term_* 系工具"
}
