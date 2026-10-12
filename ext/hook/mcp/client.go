/*
Package mcp 通过单一 mcp_router 工具封装全部 MCP 调用。
模型只看到一个 schema 恒定的工具（KV cache 友好），
server 的动态配置、工具发现、ACL 均收敛在 router 内部。
*/
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

/* ToolDef 是 MCP server 暴露的工具定义。 */
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	ArgsSchema  json.RawMessage `json:"args_schema,omitempty"`
}

/* Client 是 MCP 协议层的最小抽象，官方 sdk 的 client 实现本接口即可接入。 */
type Client interface {
	ListTools(ctx context.Context) ([]ToolDef, error)
	CallTool(ctx context.Context, name string, args json.RawMessage) (string, error)
}

/* Closer 由需要释放连接的 Client 可选实现。 */
type Closer interface {
	Close() error
}

/* ServerConfig 描述一个 MCP server 的接入方式与权限。 */
type ServerConfig struct {
	Name string
	// Description 是 server 用途说明，mcp_list 时带给模型做发现。
	Description string
	// Allow 是工具白名单；nil 表示全部允许。
	Allow []string
	// Factory 创建该 server 的 Client（懒建立）。
	Factory func(cfg ServerConfig) (Client, error)
	// DiscoverOAuth 可选：底层 401 未带 RFC9728 资源元数据时，宿主据端点探测
	// OAuth 发现文档；返回非空 metadataURL 表示该 server 需 OAuth（自动识别兜底）。
	DiscoverOAuth func(ctx context.Context) (metadataURL string, ok bool)
}

/* Config 是 mcp.Hook 的配置。 */
type Config struct {
	Servers []ServerConfig
	// Reload 可选：每次迭代回边前调用，返回新的 server 列表实现热加载。
	Reload func(ctx context.Context) ([]ServerConfig, error)
}

/* Token 是 OAuth 访问令牌（字段与底层 sdk 对齐，宿主实现 TokenStore 时透传）。 */
type Token struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresIn    int64     `json:"expires_in,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitzero"`
}

/*
TokenStore 存取单个 server 的 OAuth 令牌。宿主实现持久化（如按 server 落盘）；
无令牌时返回 ErrNoToken。同一个 server 的令牌在授权端与请求端共享。
*/
type TokenStore interface {
	GetToken(ctx context.Context) (*Token, error)
	SaveToken(ctx context.Context, token *Token) error
}

/* ErrNoToken 表示尚无令牌。 */
var ErrNoToken = errors.New("no token available")

/*
OAuthConfig 描述 HTTP 类（http/sse）server 的 OAuth 接入参数。
ClientID 为空时走动态客户端注册（DCR）；RedirectURI 指向宿主的本地回调。
*/
type OAuthConfig struct {
	ClientID              string
	ClientSecret          string
	RedirectURI           string
	Scopes                []string
	AuthServerMetadataURL string
	PKCEEnabled           bool
	TokenStore            TokenStore
}
