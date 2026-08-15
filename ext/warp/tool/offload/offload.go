// Package offload 是工具节点中间件：超大工具结果卸载到文件系统，
// 上下文里只保留头部摘要与文件路径，防止大输出（日志、转储、目录遍历）
// 撑爆上下文。写入失败时降级透传原文，绝不阻断工具执行。
package offload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

// DefaultThreshold 超过此字节数的结果触发卸载。
const DefaultThreshold = 4096

type Options struct {
	// Threshold 触发阈值，默认 4096 字节。
	Threshold int
	// Dir 卸载目标目录（FS 内路径），默认 ".ezloop/offload"。
	Dir string
	// Head 保留在消息里的原文头部长度，默认 512。
	Head int
}

// Warp 返回工具卸载中间件。
func Warp(fsys fs.FileSystem, opts ...func(*Options)) types.ToolWarpHandler {
	o := Options{Threshold: DefaultThreshold, Dir: ".ezloop/offload", Head: 512}
	for _, fn := range opts {
		fn(&o)
	}
	return func(inner types.Tool) types.Tool {
		return &offloadedTool{inner: inner, fsys: fsys, opts: o}
	}
}

type offloadedTool struct {
	inner types.Tool
	fsys  fs.FileSystem
	opts  Options
}

func (t *offloadedTool) Name() string                { return t.inner.Name() }
func (t *offloadedTool) Description() string         { return t.inner.Description() }
func (t *offloadedTool) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }

func (t *offloadedTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	result, err := t.inner.Invoke(ctx, args)
	if err != nil {
		return result, err
	}
	if len(result) <= t.opts.Threshold {
		return result, nil
	}

	// 内容哈希做文件名：同内容幂等，重复大输出不会堆积文件。
	sum := sha256.Sum256([]byte(t.inner.Name()))
	prefix := hex.EncodeToString(sum[:4])
	name := fmt.Sprintf("%s-%s.txt", t.inner.Name(), prefix)
	path := strings.TrimSuffix(t.opts.Dir, "/") + "/" + name

	if werr := t.fsys.Write(ctx, path, []byte(result)); werr != nil {
		// 卸载失败降级：宁可撑上下文也不丢结果。
		return result, nil
	}

	head := result
	if len(head) > t.opts.Head {
		head = head[:t.opts.Head]
	}
	return fmt.Sprintf("%s\n\n[输出共 %d 字节，超出 %d 字节阈值，已卸载到 %s，可用文件工具按需读取]",
		head, len(result), t.opts.Threshold, path), nil
}
