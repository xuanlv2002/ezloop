package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
)

const RouterToolName = "mcp_router"

type routerArgs struct {
	Action string          `json:"action"`
	Server string          `json:"server,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
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
}

/* Router 实现 types.Tool：对模型暴露唯一入口，内部转发到各 MCP server。 */
type Router struct {
	mu      sync.RWMutex
	servers map[string]ServerConfig
	clients map[string]Client
}

func NewRouter(servers []ServerConfig) *Router {
	m := make(map[string]ServerConfig, len(servers))
	for _, s := range servers {
		m[s.Name] = s
	}
	return &Router{servers: m, clients: make(map[string]Client)}
}

func (r *Router) Name() string { return RouterToolName }
func (r *Router) Description() string {
	return "Unified entry for all MCP tools. Discover progressively: " +
		"action=mcp_list lists servers, action=tool_list lists tools of one server (with schemas), " +
		"action=tool_call invokes one."
}

func (r *Router) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["mcp_list", "tool_list", "tool_call"]},
		"server": {"type": "string", "description": "server name, required by tool_list and tool_call"},
		"tool":   {"type": "string", "description": "tool name, required by tool_call"},
		"args":   {"type": "object", "description": "tool arguments for tool_call"}
	},
	"required": ["action"]
}`)
}

/* ReplaceServers 热加载 server 列表；工具 schema 不变，不影响缓存前缀。 */
func (r *Router) ReplaceServers(servers []ServerConfig) {
	m := make(map[string]ServerConfig, len(servers))
	for _, s := range servers {
		m[s.Name] = s
	}
	r.mu.Lock()
	r.servers = m
	r.mu.Unlock()
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

func (r *Router) Invoke(ctx context.Context, raw json.RawMessage) (string, error) {
	var args routerArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return r.errJSON(fmt.Sprintf("invalid args: %v", err), "args must be JSON matching mcp_router schema"), nil
	}
	switch args.Action {
	case "mcp_list":
		return r.mcpList(), nil
	case "tool_list":
		if args.Server == "" {
			return r.errJSON("server is required", "call mcp_list to see available servers"), nil
		}
		return r.toolList(ctx, args.Server)
	case "tool_call":
		return r.callTool(ctx, args)
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

/* toolList 连接（或复用）server 拉取该 server 的工具清单（含 schema，ACL 过滤）。 */
func (r *Router) toolList(ctx context.Context, server string) (string, error) {
	client, err := r.client(server)
	if err != nil {
		return r.errJSON(err.Error(), "call mcp_list to see available servers"), nil
	}
	defs, err := client.ListTools(ctx)
	if err != nil {
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

func (r *Router) callTool(ctx context.Context, args routerArgs) (string, error) {
	if args.Server == "" || args.Tool == "" {
		return r.errJSON("server and tool are required", "call tool_list first"), nil
	}
	if !r.allowed(args.Server, args.Tool) {
		return r.errJSON(fmt.Sprintf("tool %s/%s is not allowed", args.Server, args.Tool), "it is not in the server allow list"), nil
	}
	client, err := r.client(args.Server)
	if err != nil {
		return r.errJSON(err.Error(), "call mcp_list to see available servers"), nil
	}
	// 空串/null 也是空参（模型常传 ""），统一为 {}——部分 server 的
	// arguments 字段不接受字符串。
	if t := strings.TrimSpace(string(args.Args)); len(t) == 0 || t == "null" || t == `""` {
		args.Args = json.RawMessage(`{}`)
	}
	result, err := client.CallTool(ctx, args.Tool, args.Args)
	if err != nil {
		return r.errJSON(fmt.Sprintf("call %s/%s failed: %v", args.Server, args.Tool, err), "check tool name and args schema from tool_list"), nil
	}
	return result, nil
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
