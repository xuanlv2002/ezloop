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
			Name:  "db",
			Allow: []string{"query"},
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

// 核心链路：list_tools 的 ACL 过滤、call_tool 分发、错误结构化自纠。
func TestRouterCore(t *testing.T) {
	r := NewRouter(testCfg().Servers)

	list := invoke(t, r, `{"action":"list_tools"}`)
	if !strings.Contains(list, `"query"`) || !strings.Contains(list, `"read"`) {
		t.Fatalf("list: %s", list)
	}
	if strings.Contains(list, `"drop"`) {
		t.Fatal("db/drop must be filtered by ACL")
	}

	out := invoke(t, r, `{"action":"call_tool","server":"fs","tool":"read","args":{"path":"a"}}`)
	if out != `read({"path":"a"})` {
		t.Fatalf("call: %s", out)
	}

	denied := invoke(t, r, `{"action":"call_tool","server":"db","tool":"drop"}`)
	var ce callError
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
			testutil.ToolCalls(testutil.Call("c1", RouterToolName, `{"action":"list_tools"}`)),
			testutil.ToolCalls(testutil.Call("c2", RouterToolName, `{"action":"call_tool","server":"fs","tool":"read","args":{"path":"x"}}`)),
			testutil.Text("done"),
		),
		core.WithHooks(NewHook(cfg)),
	)
	state, err := a.Run(context.Background(), "use mcp")
	if err != nil || state.StopReason != types.StopCompleted {
		t.Fatalf("state: %s err=%v", state.StopReason, err)
	}
	if state.Messages[4].Content != `read({"path":"x"})` {
		t.Fatalf("mcp result: %s", state.Messages[4].Content)
	}
	if reload != 2 {
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
