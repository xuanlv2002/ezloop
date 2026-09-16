/*
sdk.go 基于 mark3labs/mcp-go 的接入封装，提供开箱即用的
ServerConfig.Factory，覆盖三种传输：Streamable HTTP / SSE / stdio。
*/
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	clientName     = "ezloop"
	clientVersion  = "0.1.0"
	connectTimeout = 10 * time.Second
)

/*
StreamableHTTP 返回连接 Streamable HTTP MCP server 的 Factory。
headers 会附加到每个 HTTP 请求（如 Authorization）。mcp-go 默认不建立
standalone GET SSE 流（WithContinuousListening 才开启），ezloop 只消费
ListTools/CallTool 的请求-响应，恰好够用。
*/
func StreamableHTTP(endpoint string, headers map[string]string) func(ServerConfig) (Client, error) {
	return func(ServerConfig) (Client, error) {
		var opts []transport.StreamableHTTPCOption
		if len(headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(headers))
		}
		tr, err := transport.NewStreamableHTTP(endpoint, opts...)
		if err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		return handshake(client.NewClient(tr))
	}
}

/* SSE 返回连接旧协议 HTTP SSE MCP server 的 Factory。 */
func SSE(endpoint string, headers map[string]string) func(ServerConfig) (Client, error) {
	return func(ServerConfig) (Client, error) {
		var opts []transport.ClientOption
		if len(headers) > 0 {
			opts = append(opts, transport.WithHeaders(headers))
		}
		tr, err := transport.NewSSE(endpoint, opts...)
		if err != nil {
			return nil, fmt.Errorf("connect: %w", err)
		}
		return handshake(client.NewClient(tr))
	}
}

/*
Stdio 返回通过子进程 stdio 连接 MCP server 的 Factory。command 是要
执行的命令，args 是传给它的参数；env 是附加环境变量——在继承父进程
环境的基础上合并（同名以附加为准），调用方只需填增量。
*/
func Stdio(command string, env map[string]string, args ...string) func(ServerConfig) (Client, error) {
	return func(ServerConfig) (Client, error) {
		merged := os.Environ()
		for k, v := range env {
			merged = append(merged, k+"="+v)
		}
		return handshake(client.NewClient(transport.NewStdio(command, merged, args...)))
	}
}

/*
handshake 走完 Start + Initialize（10s 超时整体覆盖），失败即关闭连接
防子进程/会话泄漏。ProtocolVersion 取 initialize 握手可协商的最高
legacy 版本，server 会按自身支持下修。
*/
func handshake(c *client.Client) (Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := c.Start(ctx); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_LEGACY_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: clientName, Version: clientVersion}
	if _, err := c.Initialize(ctx, req); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	return &mcpGoClient{client: c}, nil
}

/* mcpGoClient 用 mcp-go 的 client 实现 ezloop 的 mcp.Client。 */
type mcpGoClient struct {
	client *client.Client
}

var _ Client = (*mcpGoClient)(nil)
var _ Closer = (*mcpGoClient)(nil)

func (c *mcpGoClient) ListTools(ctx context.Context) ([]ToolDef, error) {
	res, err := c.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}
	out := make([]ToolDef, 0, len(res.Tools))
	for _, tool := range res.Tools {
		def := ToolDef{Name: tool.Name, Description: tool.Description}
		if schema := rawSchema(tool); len(schema) > 0 {
			def.ArgsSchema = schema
		}
		out = append(out, def)
	}
	return out, nil
}

/* rawSchema 优先取反序列化保真的 RawInputSchema，缺失再结构化序列化。 */
func rawSchema(tool mcp.Tool) json.RawMessage {
	if len(tool.RawInputSchema) > 0 {
		return tool.RawInputSchema
	}
	b, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return nil
	}
	return b
}

func (c *mcpGoClient) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	if len(args) > 0 {
		var arguments any
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", fmt.Errorf("invalid args for %s: %w", name, err)
		}
		req.Params.Arguments = arguments
	}
	res, err := c.client.CallTool(ctx, req)
	if err != nil {
		return "", err
	}
	return resultText(res), nil
}

func (c *mcpGoClient) Close() error { return c.client.Close() }

/*
resultText 提取文本内容；结构化输出序列化为 JSON；工具级错误
（IsError=true）以文本透传给模型自纠，不升格为 Go error。Content
元素可能是 typed TextContent 也可能是 map 形态，双路覆盖。
*/
func resultText(res *mcp.CallToolResult) string {
	if res.StructuredContent != nil {
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			return string(b)
		}
	}
	var texts []string
	for _, content := range res.Content {
		if tc, ok := mcp.AsTextContent(content); ok {
			texts = append(texts, tc.Text)
			continue
		}
		if s := mcp.GetTextFromContent(content); s != "" {
			texts = append(texts, s)
		}
	}
	return strings.Join(texts, "\n")
}
