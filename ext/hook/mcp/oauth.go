package mcp

import (
	"context"
	"strings"

	"github.com/mark3labs/mcp-go/client/transport"
)

/*
OAuthFlow 驱动单个 server 的 OAuth 流程（动态注册/授权 URL/换码），供宿主在
"MCP 页认证"与"会话内待认证"两条路径上使用。令牌经 cfg.TokenStore 落盘，
与请求端（Factory 构造的传输）共享同一份。
*/
type OAuthFlow struct {
	h *transport.OAuthHandler
}

/*
NewOAuthFlow 基于 server 的 MCP 端点与其 OAuth 配置构造流程驱动。
baseURL 用于发现授权服务器（去 query/fragment）；另有 AuthServerMetadataURL
配置时以其为准。
*/
func NewOAuthFlow(baseURL string, cfg OAuthConfig) *OAuthFlow {
	h := transport.NewOAuthHandler(toTransportOAuth(cfg))
	u := strings.TrimSpace(baseURL)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	h.SetBaseURL(u)
	return &OAuthFlow{h: h}
}

/* ClientID 返回已配置/已注册的 client id（空表示尚未注册）。 */
func (f *OAuthFlow) ClientID() string { return f.h.GetClientID() }

/* RegisterClient 走动态客户端注册（DCR）；成功后 client id 可经 ClientID 取回。 */
func (f *OAuthFlow) RegisterClient(ctx context.Context, name string) error {
	return f.h.RegisterClient(ctx, name)
}

/* SetResourceMetadataURL 用 401 的 WWW-Authenticate 指到的 RFC9728 地址喂给流程。 */
func (f *OAuthFlow) SetResourceMetadataURL(u string) {
	if u != "" {
		f.h.SetProtectedResourceMetadataURL(u)
	}
}

/* GeneratePKCE 生成 state 与 PKCE 的 verifier/challenge。 */
func (f *OAuthFlow) GeneratePKCE() (verifier, challenge, state string, err error) {
	if verifier, err = transport.GenerateCodeVerifier(); err != nil {
		return
	}
	challenge = transport.GenerateCodeChallenge(verifier)
	if state, err = transport.GenerateState(); err != nil {
		return
	}
	return
}

/* AuthorizationURL 返回授权页 URL（需已有 client id）。 */
func (f *OAuthFlow) AuthorizationURL(ctx context.Context, state, challenge string) (string, error) {
	return f.h.GetAuthorizationURL(ctx, state, challenge)
}

/* Exchange 用授权码换令牌，令牌写入 cfg.TokenStore。 */
func (f *OAuthFlow) Exchange(ctx context.Context, code, state, verifier string) error {
	return f.h.ProcessAuthorizationResponse(ctx, code, state, verifier)
}
