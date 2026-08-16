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
	Server string `json:"server"`
	Name   string `json:"name"`
	Desc   string `json:"description,omitempty"`
	Schema any    `json:"args_schema,omitempty"`
}

type callError struct {
	Error string `json:"error"`
	Hint  string `json:"hint,omitempty"`
}

// Router 实现 types.Tool：对模型暴露唯一入口，内部转发到各 MCP server。
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
	return "Unified entry for all MCP tools. Use action=list_tools to discover, action=call_tool to invoke."
}

func (r *Router) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{
	"type": "object",
	"properties": {
		"action": {"type": "string", "enum": ["list_tools", "call_tool"]},
		"server": {"type": "string", "description": "server name, required by call_tool"},
		"tool":   {"type": "string", "description": "tool name, required by call_tool"},
		"args":   {"type": "object", "description": "tool arguments"}
	},
	"required": ["action"]
}`)
}

// ReplaceServers 热加载 server 列表；工具 schema 不变，不影响缓存前缀。
func (r *Router) ReplaceServers(servers []ServerConfig) {
	m := make(map[string]ServerConfig, len(servers))
	for _, s := range servers {
		m[s.Name] = s
	}
	r.mu.Lock()
	r.servers = m
	r.mu.Unlock()
}

// Close 释放所有已建立的连接。
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
	case "list_tools":
		return r.listTools(ctx)
	case "call_tool":
		return r.callTool(ctx, args)
	default:
		return r.errJSON("unknown action: "+args.Action, `use "list_tools" or "call_tool"`), nil
	}
}

func (r *Router) listTools(ctx context.Context) (string, error) {
	r.mu.RLock()
	names := make([]string, 0, len(r.servers))
	for name := range r.servers {
		names = append(names, name)
	}
	r.mu.RUnlock()
	sort.Strings(names)

	entries := make([]toolEntry, 0)
	for _, name := range names {
		client, err := r.client(name)
		if err != nil {
			entries = append(entries, toolEntry{Server: name, Desc: "unavailable: " + err.Error()})
			continue
		}
		defs, err := client.ListTools(ctx)
		if err != nil {
			entries = append(entries, toolEntry{Server: name, Desc: "list failed: " + err.Error()})
			continue
		}
		for _, d := range defs {
			if r.allowed(name, d.Name) {
				entries = append(entries, toolEntry{
					Server: name, Name: d.Name, Desc: d.Description,
					Schema: json.RawMessage(d.ArgsSchema),
				})
			}
		}
	}
	out, _ := json.MarshalIndent(entries, "", "  ")
	return string(out), nil
}

func (r *Router) callTool(ctx context.Context, args routerArgs) (string, error) {
	if args.Server == "" || args.Tool == "" {
		return r.errJSON("server and tool are required", "call list_tools first"), nil
	}
	if !r.allowed(args.Server, args.Tool) {
		return r.errJSON(fmt.Sprintf("tool %s/%s is not allowed", args.Server, args.Tool), "it is not in the server allow list"), nil
	}
	client, err := r.client(args.Server)
	if err != nil {
		return r.errJSON(err.Error(), "call list_tools to see available servers"), nil
	}
	if len(args.Args) == 0 {
		args.Args = json.RawMessage(`{}`)
	}
	result, err := client.CallTool(ctx, args.Tool, args.Args)
	if err != nil {
		return r.errJSON(fmt.Sprintf("call %s/%s failed: %v", args.Server, args.Tool, err), "check tool name and args schema from list_tools"), nil
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
