package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/xuanlv2002/ezloop/types"
)

const RouterToolName = "mcp_router"

type routerArgs struct {
	Action string `json:"action" desc:"操作类型" enum:"mcp_list|tool_list|tool_call"`
	Server string `json:"server,omitempty" desc:"server 名，tool_list 与 tool_call 必填"`
	Tool   string `json:"tool,omitempty" desc:"工具名，tool_call 必填"`
	Args   string `json:"args,omitempty" desc:"工具参数，JSON 对象的字符串形式，tool_call 时使用"`
}

type toolEntry struct {
	Name   string `json:"name"`
	Desc   string `json:"description,omitempty"`
	Schema any    `json:"args_schema,omitempty"`
}

/* serverEntry 是 mcp_list 的条目：名字 + 配置里的用途描述。 */
type serverEntry struct {
	Name string `json:"name"`
	Desc string `json:"description,omitempty"`
}

type callError struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
	// NeedsAuth 标记该 server 未授权（前端/快应用据此引导 OAuth 认证）。
	NeedsAuth bool   `json:"needs_auth,omitempty"`
	Server    string `json:"server,omitempty"`
}

/*
AuthRequired 表示某 server 需要 OAuth 授权且当前无有效令牌。
Router 的 *WithAuth 方法返回它；工具执行路径则渲染成含 needs_auth 的文本。
*/
type AuthRequired struct {
	Server string
	// MetadataURL 是 401 通告的 RFC9728 资源元数据地址（可能为空）；直指
	// /.well-known 发现文档，授权流程据此发现授权服务器，最权威。
	MetadataURL string
}

func (e *AuthRequired) Error() string {
	return "mcp server " + e.Server + " requires OAuth authorization"
}

/*
authError 把底层错误转成 *AuthRequired（非"需 OAuth"错误返回 nil）。三种来源：
401 带资源元数据 / 已装 handler 的未授权（均直接判定）；裸 401 则请宿主探测
发现文档（ServerConfig.DiscoverOAuth）作兜底——覆盖只在 well-known 暴露、
401 不带元数据的服务。
*/
func (r *Router) authError(ctx context.Context, err error, server string) error {
	if u, ok := authRequired(err); ok {
		return &AuthRequired{Server: server, MetadataURL: u}
	}
	if bareAuthRequired(err) {
		if u, ok := r.discoverOAuth(ctx, server); ok {
			return &AuthRequired{Server: server, MetadataURL: u}
		}
	}
	return nil
}

/* discoverOAuth 请宿主探测 server 的 OAuth 发现文档（无钩子返回 false）。 */
func (r *Router) discoverOAuth(ctx context.Context, server string) (string, bool) {
	r.mu.RLock()
	cfg, ok := r.servers[server]
	r.mu.RUnlock()
	if !ok || cfg.DiscoverOAuth == nil {
		return "", false
	}
	return cfg.DiscoverOAuth(ctx)
}

/*
Router 实现 types.Tool：对模型暴露唯一入口，内部转发到各 MCP server。
工具面经嵌入的 types.Tool 提供（NewRouter 内由 NewTool 构造，schema 从
routerArgs tag 反射生成、恒定，热加载 server 列表不影响缓存前缀）。
*/
type Router struct {
	mu      sync.RWMutex
	servers map[string]ServerConfig
	clients map[string]Client
	types.Tool
}

func NewRouter(servers []ServerConfig) *Router {
	m := make(map[string]ServerConfig, len(servers))
	for _, s := range servers {
		m[s.Name] = s
	}
	r := &Router{servers: m, clients: make(map[string]Client)}
	r.Tool = types.NewTool(RouterToolName,
		"Unified entry for all MCP tools. Discover progressively: "+
			"action=mcp_list lists servers, action=tool_list lists tools of one server (with schemas), "+
			"action=tool_call invokes one.",
		func(ctx context.Context, in *routerArgs) (string, error) {
			return r.dispatch(ctx, in)
		})
	return r
}

/* ReplaceServers 热加载 server 列表，被移除 server 的旧连接随之关闭；
工具 schema 不变，不影响缓存前缀。 */
func (r *Router) ReplaceServers(servers []ServerConfig) {
	m := make(map[string]ServerConfig, len(servers))
	for _, s := range servers {
		m[s.Name] = s
	}
	r.mu.Lock()
	r.servers = m
	var dropped []Client
	for name, c := range r.clients {
		if _, ok := m[name]; !ok {
			dropped = append(dropped, c)
			delete(r.clients, name)
		}
	}
	r.mu.Unlock()
	for _, c := range dropped {
		if cl, ok := c.(Closer); ok {
			_ = cl.Close()
		}
	}
}

/*
Servers 返回当前配置的 server 清单副本（按名排序，不主动连接）。
Connected 报告某 server 是否已有活跃连接。
Drop 关闭并逐出单个 server 的连接（断开会话/失效自愈共用；
下次调用按需重建）。
Tools 懒建连（或复用）拉取某 server 的全量工具清单——不做 allow
过滤，白名单只约束模型路径（mcp_router 的 tool_list/tool_call），
用户与 API 直调看全部。
Call 直调一个工具，同样不做 allow 过滤；args 是 JSON 对象
（空/null 视为无参）。
*/
func (r *Router) Servers() []ServerConfig {
	r.mu.RLock()
	out := make([]ServerConfig, 0, len(r.servers))
	for _, cfg := range r.servers {
		out = append(out, cfg)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Router) Connected(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.clients[name]
	return ok
}

func (r *Router) Drop(name string) {
	r.mu.Lock()
	c, ok := r.clients[name]
	if ok {
		delete(r.clients, name)
	}
	r.mu.Unlock()
	if ok {
		if cl, ok := c.(Closer); ok {
			_ = cl.Close()
		}
	}
}

func (r *Router) Tools(ctx context.Context, server string) ([]ToolDef, error) {
	client, err := r.client(server)
	if err != nil {
		return nil, err
	}
	return client.ListTools(ctx)
}

func (r *Router) Call(ctx context.Context, server, tool string, args json.RawMessage) (string, error) {
	client, err := r.client(server)
	if err != nil {
		return "", err
	}
	return client.CallTool(ctx, tool, args)
}

/* Close 释放所有已建立的连接。 */
func (r *Router) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []string
	for name, c := range r.clients {
		if cl, ok := c.(Closer); ok {
			if err := cl.Close(); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", name, err))
			}
		}
	}
	r.clients = make(map[string]Client)
	if len(errs) > 0 {
		return fmt.Errorf("close: %s", strings.Join(errs, "; "))
	}
	return nil
}

func (r *Router) dispatch(ctx context.Context, args *routerArgs) (string, error) {
	switch args.Action {
	case "mcp_list":
		return r.mcpList(), nil
	case "tool_list":
		if args.Server == "" {
			return r.errJSON("server is required", "call mcp_list to see available servers"), nil
		}
		return r.toolList(ctx, args.Server)
	case "tool_call":
		return r.callTool(ctx, *args)
	default:
		return r.errJSON("unknown action: "+args.Action, `use "mcp_list", "tool_list" or "tool_call"`), nil
	}
}

/* mcpList 列出配置的 server 名单与描述（不主动连接）。 */
func (r *Router) mcpList() string {
	r.mu.RLock()
	entries := make([]serverEntry, 0, len(r.servers))
	for name, cfg := range r.servers {
		entries = append(entries, serverEntry{Name: name, Desc: cfg.Description})
	}
	r.mu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	out, _ := json.MarshalIndent(entries, "", "  ")
	return string(out)
}

/*
toolListAuth 连接（或复用）server 拉取工具清单（含 schema，ACL 过滤）。
OAuth 未授权返回 *AuthRequired，其余失败作为文本结果（nil 错误）。
*/
func (r *Router) toolListAuth(ctx context.Context, server string) (string, error) {
	c, err := r.client(server)
	if err != nil {
		if ar := r.authError(ctx, err, server); ar != nil {
			return "", ar
		}
		return r.errJSON(err.Error(), "call mcp_list to see available servers"), nil
	}
	defs, err := c.ListTools(ctx)
	if err != nil {
		if ar := r.authError(ctx, err, server); ar != nil {
			return "", ar
		}
		return r.errJSON(fmt.Sprintf("list %s failed: %v", server, err), "check server status or config"), nil
	}
	entries := make([]toolEntry, 0, len(defs))
	for _, d := range defs {
		if r.allowed(server, d.Name) {
			entries = append(entries, toolEntry{
				Name: d.Name, Desc: d.Description,
				Schema: json.RawMessage(d.ArgsSchema),
			})
		}
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	return string(out), nil
}

/* toolList 是工具执行路径：认证错误渲染为 needs_auth 文本。 */
func (r *Router) toolList(ctx context.Context, server string) (string, error) {
	out, err := r.toolListAuth(ctx, server)
	if err != nil {
		var ar *AuthRequired
		if errors.As(err, &ar) {
			return r.errJSONAuth(ar.Server, fmt.Sprintf("server %s requires OAuth authorization", ar.Server)), nil
		}
		return "", err
	}
	return out, nil
}

/*
callToolAuth 执行 tool_call 全流程（ACL 校验 + 参数归一 + 调用）。
参数/调用失败作为文本结果（nil 错误）交模型自纠；OAuth 未授权返回 *AuthRequired。
*/
func (r *Router) callToolAuth(ctx context.Context, args routerArgs) (string, error) {
	if args.Server == "" || args.Tool == "" {
		return r.errJSON("server and tool are required", "call tool_list first"), nil
	}
	if !r.allowed(args.Server, args.Tool) {
		return r.errJSON(fmt.Sprintf("tool %s/%s is not allowed", args.Server, args.Tool), "it is not in the server allow list"), nil
	}
	// args 是 JSON 文本串：空串/null/引号空串统一为 {}；非 JSON 文本
	// 回错误让模型自纠（部分 server 的 arguments 字段不接受裸字符串）。
	t := strings.TrimSpace(args.Args)
	if t == "" || t == "null" || t == `""` || t == `'{}'` {
		t = "{}"
	}
	if !json.Valid([]byte(t)) {
		return r.errJSON("args must be a JSON object string (e.g. \"{\\\"tz\\\":\\\"Asia/Shanghai\\\"}\")", "pass args as a string containing JSON"), nil
	}
	c, err := r.client(args.Server)
	if err != nil {
		if ar := r.authError(ctx, err, args.Server); ar != nil {
			return "", ar
		}
		return r.errJSON(err.Error(), "call mcp_list to see available servers"), nil
	}
	result, err := c.CallTool(ctx, args.Tool, json.RawMessage(t))
	if err != nil {
		if ar := r.authError(ctx, err, args.Server); ar != nil {
			return "", ar
		}
		return r.errJSON(fmt.Sprintf("call %s/%s failed: %v", args.Server, args.Tool, err), "check tool name and args schema from tool_list"), nil
	}
	return result, nil
}

/* callTool 是工具执行路径：认证错误渲染为 needs_auth 文本。 */
func (r *Router) callTool(ctx context.Context, args routerArgs) (string, error) {
	out, err := r.callToolAuth(ctx, args)
	if err != nil {
		var ar *AuthRequired
		if errors.As(err, &ar) {
			return r.errJSONAuth(ar.Server, fmt.Sprintf("call %s/%s requires OAuth authorization", ar.Server, args.Tool)), nil
		}
		return "", err
	}
	return out, nil
}

/*
CallWithAuth 直调一个工具（不做 allow 过滤）；server 需要 OAuth 且未授权
时返回 *AuthRequired，供 hook 与宿主 HTTP/页面路径识别并引导认证。
*/
func (r *Router) CallWithAuth(ctx context.Context, server, tool string, args json.RawMessage) (string, error) {
	return r.callToolAuth(ctx, routerArgs{Action: "tool_call", Server: server, Tool: tool, Args: string(args)})
}

/*
ToolsWithAuth 拉取工具清单（不做 allow 过滤）；server 需要 OAuth 且未授权
时返回 *AuthRequired。
*/
func (r *Router) ToolsWithAuth(ctx context.Context, server string) ([]ToolDef, error) {
	c, err := r.client(server)
	if err != nil {
		if ar := r.authError(ctx, err, server); ar != nil {
			return nil, ar
		}
		return nil, err
	}
	defs, err := c.ListTools(ctx)
	if err != nil {
		if ar := r.authError(ctx, err, server); ar != nil {
			return nil, ar
		}
		return nil, err
	}
	return defs, nil
}

func (r *Router) client(name string) (Client, error) {
	r.mu.RLock()
	cfg, ok := r.servers[name]
	if ok {
		if c, live := r.clients[name]; live {
			r.mu.RUnlock()
			return c, nil
		}
	}
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown server: %s", name)
	}
	if cfg.Factory == nil {
		return nil, fmt.Errorf("server %s has no client factory", name)
	}
	c, err := cfg.Factory(cfg)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if existing, live := r.clients[name]; live {
		// 并发建连竞争：采用先到者，关闭多余的连接避免泄漏。
		r.mu.Unlock()
		if cl, ok := c.(Closer); ok {
			_ = cl.Close()
		}
		return existing, nil
	}
	r.clients[name] = c
	r.mu.Unlock()
	return c, nil
}

func (r *Router) allowed(server, tool string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.servers[server]
	if !ok || cfg.Allow == nil {
		return ok
	}
	return slices.Contains(cfg.Allow, tool)
}

func (r *Router) errJSON(msg, hint string) string {
	out, _ := json.Marshal(callError{Error: msg, Hint: hint})
	return string(out)
}

/* errJSONAuth 渲染"需 OAuth 授权"标记文本（含 needs_auth/server，供前端与快应用识别）。 */
func (r *Router) errJSONAuth(server, msg string) string {
	out, _ := json.Marshal(callError{
		Error: msg,
		Hint:  "complete OAuth authorization for this server, then retry",
		NeedsAuth: true,
		Server:    server,
	})
	return string(out)
}

/*
authDenied 渲染"用户取消了 OAuth 授权"的工具结果。与 errJSONAuth 关键区别：
这不是"要你去认证再重试"的待办，而是**用户的决定**——因此不带 needs_auth，
且 hint 明确要求模型停手、不要重试、不要绕道用别的工具，只在用户明确要求时再调。
*/
func (r *Router) authDenied(server, reason string) string {
	msg := fmt.Sprintf("调用 %s 需要 OAuth 授权，用户已取消", server)
	if reason != "" && reason != "已取消" {
		msg += "：" + reason
	}
	out, _ := json.Marshal(callError{
		Error:  msg,
		Hint:   "the user declined authorization — do NOT retry and do NOT work around it with other tools; stop and tell the user the call was skipped. Retry only if the user explicitly asks.",
		Server: server,
	})
	return string(out)
}
