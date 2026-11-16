/*
Package offload 是 ToolEndHook：超大工具结果卸载到文件系统，上下文只留
头尾摘要与路径；写入失败时降级透传原文，绝不阻断工具执行。
*/
package offload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

/* DefaultThreshold 超过此字节数的结果触发卸载。 */
const DefaultThreshold = 4096

type Options struct {
	// Threshold 触发阈值，默认 4096 字节。
	Threshold int
	// Dir 卸载目标目录（FS 内路径），默认 ".ezloop/offload"。
	Dir string
	// Head 保留在消息里的原文头、尾长度（各 Head 字节），默认 512。
	Head int
	// Skip 免卸载名单：这些工具的结果原样保留（如分身最终答案——
	// 截断成摘要会伤主循环决策）。
	Skip []string
	// ReplayTool 回放工具名（如 read_file）：摘要尾部提示模型用它读回
	// 卸载文件全文，该工具的结果自动免卸载。空串（默认）不提示。
	ReplayTool string
	// Abs 把 FS 内相对路径渲染为提示里给出的路径（宿主注入，如按 cwd
	// 转绝对路径）。nil 时提示原样给相对路径——模型通常不知道 FS 的
	// 挂载基准，会按自己的工作目录拼错，故宿主应注入。
	Abs func(fsPath string) string
}

type Hook struct {
	fsys fs.FileSystem
	opts Options
}

/* New 创建卸载 hook，挂载后自动处理所有工具的大结果。 */
func New(fsys fs.FileSystem, opts ...func(*Options)) *Hook {
	o := Options{Threshold: DefaultThreshold, Dir: ".ezloop/offload", Head: 512}
	for _, fn := range opts {
		fn(&o)
	}
	if o.Threshold <= 0 { // 0/负数回默认：0 会让所有结果都触发卸载
		o.Threshold = DefaultThreshold
	}
	return &Hook{fsys: fsys, opts: o}
}

/* WithThreshold 设置触发阈值（字节），非正数回默认 4096。 */
func WithThreshold(n int) func(*Options) {
	return func(o *Options) { o.Threshold = n }
}

/* WithSkip 设置免卸载名单：名单内工具的结果不做大卸载。 */
func WithSkip(names ...string) func(*Options) {
	return func(o *Options) { o.Skip = names }
}

/* WithReplayTool 指定回放工具名（如 read_file），摘要尾部提示模型用它回放全文。 */
func WithReplayTool(name string) func(*Options) {
	return func(o *Options) { o.ReplayTool = name }
}

/* WithAbs 注入路径渲染函数（提示里给可直接打开的绝对路径）。 */
func WithAbs(fn func(string) string) func(*Options) {
	return func(o *Options) { o.Abs = fn }
}

func (h *Hook) Name() string { return "offload" }

func (h *Hook) OnToolEnd(ctx context.Context, _ *types.LoopState, result *types.ToolResult) error {
	if result.Err != nil || len(result.Content) <= h.opts.Threshold {
		return nil
	}
	if slices.Contains(h.opts.Skip, result.Name) || result.Name == h.opts.ReplayTool {
		return nil
	}

	// 工具名+内容联合哈希做文件名：不同内容各写各的（并发大结果互不覆盖），
	// 相同内容幂等——重复大输出不堆积文件。
	sum := sha256.Sum256(append([]byte(result.Name+"\x00"), result.Content...))
	prefix := hex.EncodeToString(sum[:6])
	name := fmt.Sprintf("%s-%s.txt", result.Name, prefix)
	path := strings.TrimSuffix(h.opts.Dir, "/") + "/" + name

	if werr := h.fsys.Write(ctx, path, []byte(result.Content)); werr != nil {
		// 卸载失败降级：宁可撑上下文也不丢结果。
		return nil
	}

	headEnd := min(h.opts.Head, len(result.Content))
	tailStart := max(len(result.Content)-h.opts.Head, headEnd)
	middle := ""
	if tailStart > headEnd {
		middle = "\n\n…（中间省略）…\n\n"
	}
	how := "可用文件工具按需读取"
	if h.opts.ReplayTool != "" {
		how = "可使用 tool " + h.opts.ReplayTool + " 进行全量内容回放"
	}
	hint := path
	if h.opts.Abs != nil {
		hint = h.opts.Abs(path)
	}
	result.Content = fmt.Sprintf("%s%s%s\n\n[输出共 %d 字节，超出 %d 字节阈值，已卸载到 %s，%s]",
		trimUTF8(result.Content[:headEnd], true), middle, trimUTF8(result.Content[tailStart:], false),
		len(result.Content), h.opts.Threshold, hint, how)
	return nil
}

/* trimUTF8 切掉被截断的多字节字符：前缀从尾部回退，后缀从头部前进。 */
func trimUTF8(s string, prefix bool) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		if prefix {
			s = s[:len(s)-1]
		} else {
			s = s[1:]
		}
	}
	return s
}
