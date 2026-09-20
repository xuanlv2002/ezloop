package mcp

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

/*
Hook 是 mcp 扩展对外的唯一入口：

	core.NewAgent(p, core.WithHooks(mcp.NewHook(cfg)))

OnStart 注册 router，OnLoop 热加载配置，OnEnd 关闭连接（仅自建
router 时——注入式连接常驻，生命周期归调用方）。

系统级单例场景用 NewHookWithRouter 注入共享 router：多个 agent
（多 session）与宿主 API 共用同一连接池。
*/
type Hook struct {
	router *Router
	cfg    Config
	owns   bool // router 是否自建（自建才在 OnEnd 关闭）
}

func NewHook(cfg Config) *Hook {
	return &Hook{router: NewRouter(cfg.Servers), cfg: cfg, owns: true}
}

/*
NewHookWithRouter 注入外部 router：hook 只使用不拥有——OnEnd 不关
连接，生命周期归调用方（系统级单例）。reload 语义同 Config.Reload，
每轮迭代回边前热加载 server 列表。
*/
func NewHookWithRouter(r *Router, reload func(context.Context) ([]ServerConfig, error)) *Hook {
	return &Hook{router: r, cfg: Config{Reload: reload}}
}

func (h *Hook) Name() string { return "mcp" }

/* OnStart 注册 router 并注入使用说明：仅当首条为 system 时追加
（无 system 的裸装配不注入，避免挪动消息序）。 */
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(h.router)
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content += "\n\n<tool-guide>\nmcp_router：访问外部能力（已配置的 MCP server）的统一入口。" +
			"用法两步：先 mcp_list 看服务清单、tool_list 拉取目标服务的工具清单（含各工具参数 schema），" +
			"再 tool_call 调用——不要凭记忆猜工具名或参数。内置工具能做的事不必绕道 MCP。" +
			"服务清单以 system 的 <mcp> 段为准，轮内变更看 <resource_change>，不要读配置文件发现服务。\n</tool-guide>"
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
	if !h.owns {
		return nil
	}
	return h.router.Close()
}
