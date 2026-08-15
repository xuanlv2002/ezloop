// Package mcp 通过单一 mcp_router 工具封装全部 MCP 调用。
// 模型只看到一个 schema 恒定的工具（KV cache 友好），
// server 的动态配置、工具发现、ACL 均收敛在 router 内部。
package mcp

import (
	"context"
	"encoding/json"
)

// ToolDef 是 MCP server 暴露的工具定义。
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	ArgsSchema  json.RawMessage `json:"args_schema,omitempty"`
}

// Client 是 MCP 协议层的最小抽象，官方 sdk 的 client 实现本接口即可接入。
type Client interface {
	ListTools(ctx context.Context) ([]ToolDef, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)
}

// Closer 由需要释放连接的 Client 可选实现。
type Closer interface {
	Close() error
}

// ServerConfig 描述一个 MCP server 的接入方式与权限。
type ServerConfig struct {
	Name string
	// Allow 是工具白名单；nil 表示全部允许。
	Allow []string
	// Factory 创建该 server 的 Client（懒建立）。
	Factory func(cfg ServerConfig) (Client, error)
}

// Config 是 mcp.Hook 的配置。
type Config struct {
	Servers []ServerConfig
	// Reload 可选：每次迭代回边前调用，返回新的 server 列表实现热加载。
	Reload func(ctx context.Context) ([]ServerConfig, error)
}
