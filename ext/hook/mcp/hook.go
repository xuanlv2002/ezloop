package mcp

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/hook/internal/await"
	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

/*
EventAuthRequest 是 OAuth 授权请求事件，Data 为 AuthRequestData：
工具调用命中需认证的 server 时发出，宿主持久化呈现并引导认证，完成后
经决策 channel 回传，该次调用随即重试。
*/
const EventAuthRequest = event.EventType("mcp.auth.request")

/* AuthRequestData 是授权请求载荷；ID 供 pending 登记与决策关联，MetadataURL 供发现授权服务器。 */
type AuthRequestData struct {
	ID          string `json:"id"`
	Server      string `json:"server"`
	MetadataURL string `json:"metadata_url,omitempty"`
}

/* AuthDecision 是对一次授权请求的回应（CallID 留空视为回应当前请求）。 */
type AuthDecision struct {
	CallID string
	OK     bool
	Err    string
}

/*
Hook 是 mcp 扩展对外的唯一入口：

	core.NewAgent(p, core.WithHooks(mcp.NewHook(cfg)))

OnStart 注册 router，OnLoop 热加载配置，OnEnd 关闭连接（仅自建
router 时——注入式连接常驻，生命周期归调用方）。

系统级单例场景用 NewHookWithRouter 注入共享 router：多个 agent
（多 session）与宿主 API 共用同一连接池。

配置了 Authorize 时，OnToolStart 拦截需认证的 mcp_router 调用并阻塞
至宿主完成授权；构造函数返回决策 channel 供宿主回传。
*/
type Hook struct {
	router *Router
	cfg    Config
	owns   bool // router 是否自建（自建才在 OnEnd 关闭）
	authr  *await.Router[AuthDecision]
}

func NewHook(cfg Config) (*Hook, chan<- AuthDecision) {
	ch := make(chan AuthDecision)
	return &Hook{
		router: NewRouter(cfg.Servers),
		cfg:    cfg,
		owns:   true,
		authr:  await.New(ch, func(d AuthDecision) string { return d.CallID }),
	}, ch
}

/*
NewHookWithRouter 注入外部 router：hook 只使用不拥有——OnEnd 不关
连接，生命周期归调用方（系统级单例）。reload 语义同 Config.Reload，
每轮迭代回边前热加载 server 列表。返回的 channel 供宿主回传授权决策。
*/
func NewHookWithRouter(r *Router, reload func(context.Context) ([]ServerConfig, error)) (*Hook, chan<- AuthDecision) {
	ch := make(chan AuthDecision)
	return &Hook{
		router: r,
		cfg:    Config{Reload: reload},
		authr:  await.New(ch, func(d AuthDecision) string { return d.CallID }),
	}, ch
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

/*
OnToolStart 拦截 mcp_router 的 tool_list / tool_call：命中需 OAuth 的
server 时经 state 发授权请求并阻塞等待宿主的决策（AuthDecision），授权
成功后重试一次；结果以 Skip 写入（后续 toolEnd hook 如 offload 照常处理）。
mcp_list 无需认证，直接放行交工具正常执行。
*/
func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (hook.Action, error) {
	if call.Name != RouterToolName {
		return hook.Proceed, nil
	}
	var args routerArgs
	if json.Unmarshal(call.Args, &args) != nil {
		return hook.Proceed, nil
	}
	switch args.Action {
	case "tool_list", "tool_call":
	default:
		return hook.Proceed, nil
	}
	out, err := h.runWithAuth(ctx, state, call, args)
	if err != nil {
		return hook.Skip(""), err
	}
	return hook.Skip(out), nil
}

/* runWithAuth 执行一次调用；遇认证失败则发请求、等决策、成功则重试一次。 */
func (h *Hook) runWithAuth(ctx context.Context, state *types.LoopState, call *types.ToolCall, args routerArgs) (string, error) {
	invoke := func() (string, error) {
		if args.Action == "tool_list" {
			return h.router.toolListAuth(ctx, args.Server)
		}
		return h.router.callToolAuth(ctx, args)
	}
	out, err := invoke()
	if err == nil {
		return out, nil
	}
	var ar *AuthRequired
	if !errors.As(err, &ar) {
		return "", err
	}
	state.EmitEvent(EventAuthRequest, AuthRequestData{ID: call.ID, Server: ar.Server, MetadataURL: ar.MetadataURL})
	d, ok := h.authr.Await(ctx, call.ID)
	if !ok {
		return h.router.errJSONAuth(ar.Server, "authorization cancelled"), nil
	}
	if !d.OK {
		msg := "authorization declined"
		if d.Err != "" {
			msg = d.Err
		}
		return h.router.errJSONAuth(ar.Server, msg), nil
	}
	out, err = invoke()
	if err == nil {
		return out, nil
	}
	if errors.As(err, &ar) {
		return h.router.errJSONAuth(ar.Server, "still requires OAuth authorization after the authorization flow"), nil
	}
	return "", err
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
