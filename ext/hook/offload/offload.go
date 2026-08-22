/*
Package offload 是 ToolEndHook：超大工具结果卸载到文件系统，
上下文里只保留头部摘要与文件路径，防止大输出（日志、转储、目录遍历）
撑爆上下文。写入失败时降级透传原文，绝不阻断工具执行。
结果后处理不耦合工具节点本身，故为 hook 而非 warp。
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

func (h *Hook) Name() string { return "offload" }

func (h *Hook) OnToolEnd(ctx context.Context, _ *types.LoopState, result *types.ToolResult) error {
	if result.Err != nil || len(result.Content) <= h.opts.Threshold {
		return nil
	}
	if slices.Contains(h.opts.Skip, result.Name) {
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
	result.Content = fmt.Sprintf("%s\n\n[输出共 %d 字节，超出 %d 字节阈值，已卸载到 %s，可用文件工具按需读取]",
		head, len(result.Content), h.opts.Threshold, path)
	return nil
}
