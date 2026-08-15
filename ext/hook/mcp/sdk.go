// sdk.go 基于官方 go-sdk (modelcontextprotocol/go-sdk) 的接入封装，
// 提供开箱即用的 ServerConfig.Factory。
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	clientName    = "ezloop"
	clientVersion = "0.1.0"
	connectTimeout = 10 * time.Second
)

// StreamableHTTP 返回连接 Streamable HTTP MCP server 的 Factory。
// headers 会附加到每个 HTTP 请求（如 Authorization）。
func StreamableHTTP(endpoint string, headers map[string]string) func(ServerConfig) (Client, error) {
	return func(ServerConfig) (Client, error) {
		httpClient := http.DefaultClient
		if len(headers) > 0 {
			httpClient = &http.Client{Transport: &headerTransport{
				base:    http.DefaultTransport,
				headers: headers,
			}}
		}
		transport := &sdkmcp.StreamableClientTransport{
			Endpoint:   endpoint,
			HTTPClient: httpClient,
		}
		return connectSDK(transport)
	}
}

// Stdio 返回通过子进程 stdio 连接 MCP server 的 Factory。
func Stdio(name string, args ...string) func(ServerConfig) (Client, error) {
	return func(ServerConfig) (Client, error) {
		return connectSDK(&sdkmcp.CommandTransport{Command: exec.Command(name, args...)})
	}
}

// WrapSession 将官方 SDK 的已连接会话包装为 mcp.Client（高级用法/测试用）。
func WrapSession(session *sdkmcp.ClientSession) Client { return &sdkClient{session: session} }

func connectSDK(transport sdkmcp.Transport) (Client, error) {
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: clientName, Version: clientVersion}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp: connect: %w", err)
	}
	return &sdkClient{session: session}, nil
}

// sdkClient 用官方 SDK 的 ClientSession 实现 ezloop 的 mcp.Client。
type sdkClient struct {
	session *sdkmcp.ClientSession
}

var _ Client = (*sdkClient)(nil)
var _ Closer = (*sdkClient)(nil)

func (c *sdkClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	res, err := c.session.ListTools(ctx, nil)
	if err != nil {
		return nil, err
	}
	out := make([]ToolDef, 0, len(res.Tools))
	for _, tool := range res.Tools {
		def := ToolDef{Name: tool.Name, Description: tool.Description}
		if tool.InputSchema != nil {
			if b, merr := json.Marshal(tool.InputSchema); merr == nil {
				def.ArgsSchema = b
			}
		}
		out = append(out, def)
	}
	return out, nil
}

func (c *sdkClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	var arguments any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", fmt.Errorf("mcp: invalid args for %s: %w", name, err)
		}
	}
	res, err := c.session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return "", err
	}
	return resultText(res), nil
}

func (c *sdkClient) Close() error { return c.session.Close() }

// resultText 提取文本内容；结构化输出序列化为 JSON。
func resultText(res *sdkmcp.CallToolResult) string {
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			return string(b)
		}
	}
	text := ""
	for _, content := range res.Content {
		if tc, ok := content.(*sdkmcp.TextContent); ok {
			if text != "" {
				text += "\n"
			}
			text += tc.Text
		}
	}
	return text
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	for k, v := range t.headers {
		clone.Header.Set(k, v)
	}
	return t.base.RoundTrip(clone)
}
