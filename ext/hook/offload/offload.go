/*
Package offload 是 ToolEndHook：超大工具结果卸载到文件系统，
上下文里只保留头部摘要与文件路径，防止大输出（日志、转储、目录遍历）
撑爆上下文。写入失败时降级透传原文，绝不阻断工具执行。
结果后处理不耦合工具节点本身，故为 hook 而非 warp。

配置 ReplayTool（如 read_file）后，摘要尾部提示模型用该工具回放
卸载文件全文，且该工具自身的结果免卸载——回放的意义就是把全文
带回上下文，再卸载即死循环。
*/
package offload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

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
	// Head 保留在消息里的原文头部长度，默认 512。
	Head int
	// Skip 免卸载名单：这些工具的结果原样保留（如分身最终答案——
	// 截断成摘要会伤主循环决策）。
	Skip []string
	// ReplayTool 回放工具名（如 read_file）：摘要尾部提示模型用它读回
	// 卸载文件全文，该工具的结果自动免卸载。空串（默认）不提示。
	ReplayTool string
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
	return &Hook{fsys: fsys, opts: o}
}

/* WithSkip 设置免卸载名单：名单内工具的结果不做大卸载。 */
func WithSkip(names ...string) func(*Options) {
	return func(o *Options) { o.Skip = names }
}

/* WithReplayTool 指定回放工具名（如 read_file），摘要尾部提示模型用它回放全文。 */
func WithReplayTool(name string) func(*Options) {
	return func(o *Options) { o.ReplayTool = name }
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

	head := result.Content
	if len(head) > h.opts.Head {
		head = head[:h.opts.Head]
	}
	tail := "可用文件工具按需读取"
	if h.opts.ReplayTool != "" {
		tail = "可使用 tool " + h.opts.ReplayTool + " 进行全量内容回放"
	}
	result.Content = fmt.Sprintf("%s\n\n[输出共 %d 字节，超出 %d 字节阈值，已卸载到 %s，%s]",
		head, len(result.Content), h.opts.Threshold, path, tail)
	return nil
}
