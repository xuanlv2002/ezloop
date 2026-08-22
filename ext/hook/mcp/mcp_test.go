package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/internal/testutil"
	"github.com/xuanlv2002/ezloop/types"
)

type mockClient struct{ tools []ToolDef }

func (c *mockClient) ListTools(_ context.Context) ([]ToolDef, error) { return c.tools, nil }
func (c *mockClient) CallTool(_ context.Context, name string, args json.RawMessage) (string, error) {
	return fmt.Sprintf("%s(%s)", name, string(args)), nil
}

func testCfg() Config {
	return Config{Servers: []ServerConfig{
		{
			Name:        "db",
			Description: "database access",
			Allow:       []string{"query"},
			Factory: func(ServerConfig) (Client, error) {
				return &mockClient{tools: []ToolDef{
					{Name: "query", ArgsSchema: json.RawMessage(`{"type":"object"}`)},
					{Name: "drop"},
				}}, nil
			},
		},
		{
			Name: "fs",
			Factory: func(ServerConfig) (Client, error) {
				return &mockClient{tools: []ToolDef{{Name: "read"}}}, nil
			},
		},
	}}
}

func invoke(t *testing.T, r *Router, args string) string {
	t.Helper()
	out, err := r.Invoke(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	return out
}

// 核心链路：三层渐进发现（mcp_list → tool_list → tool_call）、ACL 过滤、错误结构化自纠。
func TestRouterCore(t *testing.T) {
	r := NewRouter(testCfg().Servers)

	servers := invoke(t, r, `{"action":"mcp_list"}`)
	if !strings.Contains(servers, `"db"`) || !strings.Contains(servers, `"fs"`) {
		t.Fatalf("mcp_list: %s", servers)
	}
	if !strings.Contains(servers, `"database access"`) {
		t.Fatalf("mcp_list should include description: %s", servers)
	}

	dbTools := invoke(t, r, `{"action":"tool_list","server":"db"}`)
	if !strings.Contains(dbTools, `"query"`) || strings.Contains(dbTools, `"drop"`) {
		t.Fatalf("tool_list db must be ACL-filtered: %s", dbTools)
	}

	out := invoke(t, r, `{"action":"tool_call","server":"fs","tool":"read","args":{"path":"a"}}`)
	if out != `read({"path":"a"})` {
		t.Fatalf("call: %s", out)
	}

	// 空字符串 args 统一为 {}（模型常传 ""）
	empty := invoke(t, r, `{"action":"tool_call","server":"fs","tool":"read","args":""}`)
	if empty != `read({})` {
		t.Fatalf("empty args should normalize to {}: %s", empty)
	}

	unknown := invoke(t, r, `{"action":"tool_list","server":"nope"}`)
	var ce callError
	_ = json.Unmarshal([]byte(unknown), &ce)
	if ce.Error == "" || ce.Hint == "" {
		t.Fatalf("unknown server should be structured: %s", unknown)
	}

	denied := invoke(t, r, `{"action":"tool_call","server":"db","tool":"drop"}`)
	_ = json.Unmarshal([]byte(denied), &ce)
	if ce.Error == "" || ce.Hint == "" {
		t.Fatalf("acl denial should be structured: %s", denied)
	}
}

// Hook 注入 + 完整 loop：模型经 mcp_router 自发现并调用。
func TestHookFullLoop(t *testing.T) {
	cfg := testCfg()
	reload := 0
	cfg.Reload = func(context.Context) ([]ServerConfig, error) {
		reload++
		return cfg.Servers, nil
	}
	a := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("c1", RouterToolName, `{"action":"mcp_list"}`)),
			testutil.ToolCalls(testutil.Call("c2", RouterToolName, `{"action":"tool_list","server":"fs"}`)),
			testutil.ToolCalls(testutil.Call("c3", RouterToolName, `{"action":"tool_call","server":"fs","tool":"read","args":{"path":"x"}}`)),
			testutil.Text("done"),
		),
		core.WithHooks(NewHook(cfg)),
	)
	state, err := a.Run(context.Background(), "use mcp")
	if err != nil || state.StopReason != types.StopCompleted {
		t.Fatalf("state: %s err=%v", state.StopReason, err)
	}
	if state.Messages[6].Content != `read({"path":"x"})` {
		t.Fatalf("mcp result: %s", state.Messages[6].Content)
	}
	if reload != 3 {
		t.Fatalf("reloads: %d", reload)
	}
}

// 官方 go-sdk 内存会话：真实协议栈的 list/call。
func TestSDKSession(t *testing.T) {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "t"}, nil)
	type greetArgs struct {
		Name string `json:"name"`
	}
	sdkmcp.AddTool(server, &sdkmcp.Tool{Name: "greet"}, func(_ context.Context, _ *sdkmcp.CallToolRequest, args greetArgs) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: "hi " + args.Name}},
		}, nil, nil
	})
	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	go func() { _ = server.Run(context.Background(), serverTransport) }()

	client, err := connectSDK(clientTransport)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if cl, ok := client.(Closer); ok {
		defer cl.Close()
	}

	defs, err := client.ListTools(context.Background())
	if err != nil || len(defs) != 1 || defs[0].Name != "greet" || len(defs[0].ArgsSchema) == 0 {
		t.Fatalf("list: %+v err=%v", defs, err)
	}
	out, err := client.CallTool(context.Background(), "greet", json.RawMessage(`{"name":"ezloop"}`))
	if err != nil || out != "hi ezloop" {
		t.Fatalf("call: %q %v", out, err)
	}
}
