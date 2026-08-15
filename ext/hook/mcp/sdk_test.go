package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func closeClient(c Client) {
	if cl, ok := c.(Closer); ok {
		_ = cl.Close()
	}
}

// newInMemoryServer 起一个带 greet 工具的内存 MCP server，返回 client 侧 transport。
func newInMemoryServer(t *testing.T) *sdkmcp.InMemoryTransport {
	t.Helper()
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-server"}, nil)
	type greetArgs struct {
		Name string `json:"name"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "greet", Description: "say hi"}, func(_ context.Context, _ *sdkmcp.CallToolRequest, args greetArgs) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hi " + args.Name}},
		}, nil, nil
	})
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	go func() { _ = server.Run(context.Background(), serverTransport) }()
	return clientTransport
}

func TestSDKClientListAndCall(t *testing.T) {
	transport := newInMemoryServer(t)
	client, err := connectSDK(transport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer closeClient(client)

	defs, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(defs) != 1 || defs[0].Name != "greet" {
		t.Fatalf("tools: %+v", defs)
	}
	if len(defs[0].ArgsSchema) == 0 {
		t.Fatal("want non-empty args schema")
	}

	out, err := client.CallTool(context.Background(), "greet", json.RawMessage(`{"name":"ezloop"}`))
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "hi ezloop" {
		t.Fatalf("result: %q", out)
	}
}

// Router 经官方 SDK 会话的完整链路。
func TestRouterOverSDKSession(t *testing.T) {
	transport := newInMemoryServer(t)
	client, err := connectSDK(transport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer closeClient(client)

	r := NewRouter([]ServerConfig{{
		Name:    "mem",
		Factory: func(ServerConfig) (Client, error) { return client, nil },
	}})

	out, err := r.Invoke(context.Background(), json.RawMessage(
		`{"action":"call_tool","server":"mem","tool":"greet","args":{"name":"router"}}`))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if !strings.Contains(out, "hi router") {
		t.Fatalf("result: %q", out)
	}

	list, err := r.Invoke(context.Background(), json.RawMessage(`{"action":"list_tools"}`))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(list, `"greet"`) {
		t.Fatalf("list: %s", list)
	}
}
