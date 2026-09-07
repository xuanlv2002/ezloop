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
	"encoding/base64"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

type Hook struct {
	fsys    fs.FileSystem
	workDir string // terminal 执行目录（空 = 进程 cwd）
	onImage func(path, mime string) (text string, loadAsImage bool) // 图片分支裁决（nil = 默认插图）

	mu    sync.Mutex
	locks map[string]*sync.Mutex // per-path 修改队列
}

/* New 创建文件与终端工具 hook；WithWorkDir 可指定 terminal 执行目录，
WithImageHandler 可裁决 read_file 的图片分支。 */
func New(fsys fs.FileSystem, opts ...Option) *Hook {
	h := &Hook{fsys: fsys, locks: make(map[string]*sync.Mutex)}
	for _, o := range opts {
		o(h)
	}
	return h
}

/* Option 是 hook 装配选项。 */
type Option func(*Hook)

/* WithWorkDir 设定 terminal 工具的执行目录（空串保持进程 cwd）。 */
func WithWorkDir(dir string) Option {
	return func(h *Hook) { h.workDir = dir }
}

/* WithImageHandler 裁决 read_file 的图片分支：读到图片（魔数判定）时
不再按文本分页（乱码）。loadAsImage=true 走默认的标记→OnLoop 转换
（持久化 user 图片消息）；false 返回 text 作为普通工具结果（如无视觉
模型引导改用识别工具）。 */
func WithImageHandler(fn func(path, mime string) (text string, loadAsImage bool)) Option {
	return func(h *Hook) { h.onImage = fn }
}

func (h *Hook) Name() string { return "filetools" }

func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(readTool(h))
	state.Tools.Register(writeTool(h))
	state.Tools.Register(editTool(h))
	state.Tools.Register(terminalTool(h.workDir))
	injectOSHint(state)
	return nil
}

/* EventImageLoaded 是图片进上下文事件（Data 为 []string 路径）：
加载点即事实源——前端据此实时渲染，与历史 <image_loaded> 消息同款。 */
const EventImageLoaded = event.EventType("filetools.image_loaded")

/*
OnLoop 把本轮工具批的 image_loaded 标记转换为持久化的 user 图片消息。

本轮批的边界由消息结构天然划定：从尾部向前收集 tool 消息，遇到第一条
assistant 停止（批内 tool_use 的载体）。批内全部标记对应的图片合并为
一条 user 消息：

	<image_loaded>
	C:/路径1
	C:/路径2
	</image_loaded>

（Images 携带 base64），插在批的最后一条 tool 消息之后，随历史落盘
——重启后 provider 直接带图。插入成功即发 EventImageLoaded 事件。

已入史的 tool 结果不改写：标记文本自解释（图片消息紧随其后），加载
失败的标记留着（模型见标记无图自会重读）。不会重复加载：先有
assistant（tool_use）才有 tool 结果，上一批之后必有新的 assistant
边界挡住回扫；取消轮残留进历史的标记不补偿。
*/
func (h *Hook) OnLoop(ctx context.Context, state *types.LoopState) error {
	var toolIdx []int // 本轮批的 tool 消息下标（从尾往前收集）
	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == types.RoleAssistant {
			break
		}
		if state.Messages[i].Role == types.RoleTool {
			toolIdx = append(toolIdx, i)
		}
	}
	if len(toolIdx) == 0 {
		return nil
	}
	slices.Reverse(toolIdx) // 恢复批内时间序（与 tool_use 对应顺序一致）
	var imgs []types.ImagePart
	var paths []string
	for _, i := range toolIdx {
		for _, match := range imageMarkRe.FindAllStringSubmatch(state.Messages[i].Content, -1) {
			if img, ok := h.loadImage(ctx, match[1]); ok {
				imgs = append(imgs, img)
				paths = append(paths, match[1])
			}
		}
	}
	if len(imgs) == 0 {
		return nil
	}
	last := toolIdx[len(toolIdx)-1] // 恢复正序后，末位即批内最大下标
	msg := types.Message{
		Role: types.RoleUser,
		Content: "<" + imageLoadedTag + ">\n" + strings.Join(paths, "\n") +
			"\n</" + imageLoadedTag + ">",
		Images: imgs,
	}
	state.Messages = slices.Insert(state.Messages, last+1, msg)
	state.EmitEvent(EventImageLoaded, paths)
	return nil
}

/* loadImage 读盘并转为 ImagePart（魔数推 MIME、上限校验）。 */
func (h *Hook) loadImage(ctx context.Context, path string) (types.ImagePart, bool) {
	data, err := h.fsys.Read(ctx, path)
	if err != nil || len(data) > readMaxImageBytes {
		return types.ImagePart{}, false
	}
	mime := imageMime(data)
	if mime == "" {
		return types.ImagePart{}, false
	}
	return types.ImagePart{MimeType: mime, Data: base64.StdEncoding.EncodeToString(data)}, true
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
