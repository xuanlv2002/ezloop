package mcp

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

/*
Hook 是 mcp 扩展对外的唯一入口：

	core.NewAgent(p, core.WithHooks(mcp.NewHook(cfg)))

OnStart 注册 router，OnLoop 热加载配置，OnEnd 关闭连接。
*/
type Hook struct {
	router *Router
	cfg    Config
}

func NewHook(cfg Config) *Hook {
	return &Hook{router: NewRouter(cfg.Servers), cfg: cfg}
}

func (h *Hook) Name() string { return "mcp" }

/* OnStart 注册 router 并注入使用说明：仅当首条为 system 时追加
（无 system 的裸装配不注入，避免挪动消息序）。 */
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(h.router)
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content += "\n\n<tool-guide>\nmcp_router：访问外部能力（已配置的 MCP server）统一入口，" +
			"先 mcp_list / tool_list 发现可用能力，再 tool_call 调用；内置工具能做的事不必绕道 MCP。\n</tool-guide>"
	}
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
