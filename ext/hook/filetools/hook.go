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
	"runtime"
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
	state.Tools.Register(readTool(h.fsys))
	state.Tools.Register(writeTool(h))
	state.Tools.Register(editTool(h))
	state.Tools.Register(terminalTool())
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
