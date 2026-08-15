package mcp

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

// Hook 是 mcp 扩展对外的唯一入口：
//
//	core.NewAgent(p, core.WithHooks(mcp.NewHook(cfg)))
//
// OnStart 注册 router，OnLoop 热加载配置，OnEnd 关闭连接。
type Hook struct {
	router *Router
	cfg    Config
}

func NewHook(cfg Config) *Hook {
	return &Hook{router: NewRouter(cfg.Servers), cfg: cfg}
}

func (h *Hook) Name() string { return "mcp" }

func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(h.router)
	return nil
}

func (h *Hook) OnLoop(ctx context.Context, _ *types.LoopState) error {
	if h.cfg.Reload == nil {
		return nil
	}
	servers, err := h.cfg.Reload(ctx)
	if err != nil {
		return err
	}
	h.router.ReplaceServers(servers)
	return nil
}

func (h *Hook) OnEnd(_ context.Context, _ *types.LoopState) error {
	return h.router.Close()
}
