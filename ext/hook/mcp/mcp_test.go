package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpserver "github.com/mark3labs/mcp-go/server"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/hook"
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

	out := invoke(t, r, `{"action":"tool_call","server":"fs","tool":"read","args":"{\"path\":\"a\"}"}`)
	if out != `read({"path":"a"})` {
		t.Fatalf("call: %s", out)
	}

	// 空字符串 args 统一为 {}（模型常传 ""）
	empty := invoke(t, r, `{"action":"tool_call","server":"fs","tool":"read","args":""}`)
	if empty != `read({})` {
		t.Fatalf("empty args should normalize to {}: %s", empty)
	}

	// 非 JSON 文本的 args：结构化错误回传模型自纠
	bad := invoke(t, r, `{"action":"tool_call","server":"fs","tool":"read","args":"abc"}`)
	var badErr callError
	_ = json.Unmarshal([]byte(bad), &badErr)
	if badErr.Error == "" || badErr.Hint == "" {
		t.Fatalf("non-JSON args should be structured: %s", bad)
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
	h, _ := NewHook(cfg)
	a := core.NewAgent(
		testutil.Scripted(
			testutil.ToolCalls(testutil.Call("c1", RouterToolName, `{"action":"mcp_list"}`)),
			testutil.ToolCalls(testutil.Call("c2", RouterToolName, `{"action":"tool_list","server":"fs"}`)),
			testutil.ToolCalls(testutil.Call("c3", RouterToolName, `{"action":"tool_call","server":"fs","tool":"read","args":"{\"path\":\"x\"}"}`)),
			testutil.Text("done"),
		),
		core.WithHooks(h),
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

// 注入式 router：hook OnEnd 不关闭连接（生命周期归调用方）；自建式才关。
func TestHookWithRouterKeepsConnections(t *testing.T) {
	state := &types.LoopState{Tools: types.NewToolRegistry(), Metadata: map[string]any{}}

	r := NewRouter(nil)
	r.clients["db"] = &mockClient{tools: []ToolDef{{Name: "query"}}}
	h, _ := NewHookWithRouter(r, nil)
	if err := h.OnEnd(context.Background(), state); err != nil {
		t.Fatalf("onend: %v", err)
	}
	if !r.Connected("db") {
		t.Fatal("injected router must keep connections after hook OnEnd")
	}

	owned, _ := NewHook(Config{})
	owned.router.clients["db"] = &mockClient{}
	if err := owned.OnEnd(context.Background(), state); err != nil {
		t.Fatalf("owned onend: %v", err)
	}
	if owned.router.Connected("db") {
		t.Fatal("owned router must be closed on hook OnEnd")
	}
}

// ReplaceServers：被移除 server 的连接随之关闭，保留的不受影响。
func TestReplaceServersDropsRemoved(t *testing.T) {
	r := NewRouter(nil)
	r.clients["a"] = &mockClient{}
	r.clients["b"] = &mockClient{}
	r.ReplaceServers([]ServerConfig{{Name: "b"}, {Name: "c"}})
	if r.Connected("a") {
		t.Fatal("removed server connection must be dropped")
	}
	if !r.Connected("b") {
		t.Fatal("kept server connection must survive")
	}
}

// 需 OAuth 的 server：CallWithAuth 报 *AuthRequired；Hook 拦截后等决策、授权重试成功。
func TestAuthRequiredAndHookRetry(t *testing.T) {
	fcDirect := &flakyAuthClient{}
	r := NewRouter([]ServerConfig{{Name: "s", Factory: func(ServerConfig) (Client, error) { return fcDirect, nil }}})
	_, err := r.CallWithAuth(context.Background(), "s", "read", json.RawMessage(`{}`))
	var ar *AuthRequired
	if !errors.As(err, &ar) || ar.Server != "s" {
		t.Fatalf("want *AuthRequired for s, got %v", err)
	}

	fc := &flakyAuthClient{}
	h, decisions := NewHook(Config{Servers: []ServerConfig{{Name: "s", Factory: func(ServerConfig) (Client, error) { return fc, nil }}}})
	var got []event.Event
	state := &types.LoopState{Tools: types.NewToolRegistry(), Metadata: map[string]any{},
		Emitter: func(e event.Event) { got = append(got, e) }}
	call := &types.ToolCall{ID: "c1", Name: RouterToolName, Args: json.RawMessage(`{"action":"tool_call","server":"s","tool":"read","args":"{}"}`)}

	// 首次调用必失败（calls==1）→ 发请求 → 等决策；授权决策回传后重试（calls==2）成功。
	// 决策早于 Await 到达也无妨：await.Router 按 key 暂存。
	go func() { decisions <- AuthDecision{CallID: "c1", OK: true} }()
	action, err := h.OnToolStart(context.Background(), state, call)
	if err != nil || action.Kind != hook.KindSkip || action.Result != `read({})` {
		t.Fatalf("action=%v result=%q err=%v", action.Kind, action.Result, err)
	}
	if len(got) != 1 || got[0].Type != EventAuthRequest {
		t.Fatalf("auth request event: %+v", got)
	}

	// 取消/拒绝：结果为"用户已取消"（不含 needs_auth——那是待办，这是用户的决定），指示模型停手。
	fc2 := &flakyAuthClient{}
	h2, decisions2 := NewHook(Config{Servers: []ServerConfig{{Name: "s", Factory: func(ServerConfig) (Client, error) { return fc2, nil }}}})
	go func() { decisions2 <- AuthDecision{CallID: "c1", OK: false} }()
	a2, err := h2.OnToolStart(context.Background(), state, call)
	if err != nil || a2.Kind != hook.KindSkip || strings.Contains(a2.Result, "needs_auth") || !strings.Contains(a2.Result, "declined") {
		t.Fatalf("declined: action=%v result=%q err=%v", a2.Kind, a2.Result, err)
	}
}

// 未预装 handler 的 401：通告了 RFC9728 资源元数据才判定需 OAuth，否则按普通错误。
func TestAuthRequiredSignal(t *testing.T) {
	if _, ok := authRequired(&transport.AuthorizationRequiredError{}); ok {
		t.Fatal("裸 401（无 resource_metadata）不应判定为需 OAuth")
	}
	withMeta := &transport.AuthorizationRequiredError{ResourceMetadataURL: "https://x/.well-known/oauth-protected-resource"}
	if u, ok := authRequired(withMeta); !ok || u == "" {
		t.Fatalf("带 resource_metadata 的 401 应判定需 OAuth，got %q %v", u, ok)
	}
}

// 裸 401（无资源元数据）：有 DiscoverOAuth 钩子命中则判定需 OAuth，否则按普通错误。
func TestBareAuthDiscoveryFallback(t *testing.T) {
	makeRouter := func(discover func(context.Context) (string, bool)) *Router {
		return NewRouter([]ServerConfig{{
			Name:          "s",
			Factory:       func(ServerConfig) (Client, error) { return &bareAuthClient{}, nil },
			DiscoverOAuth: discover,
		}})
	}

	// 无发现钩子：裸 401 按普通错误文本（nil error）返回
	out, err := makeRouter(nil).CallWithAuth(context.Background(), "s", "read", json.RawMessage(`{}`))
	if err != nil || !strings.Contains(out, "call s/read failed") {
		t.Fatalf("no discover hook: out=%q err=%v", out, err)
	}

	// 钩子命中：判定需 OAuth，带出发现文档 URL
	r := makeRouter(func(context.Context) (string, bool) { return "https://h/.well-known/oauth-protected-resource", true })
	var ar *AuthRequired
	if _, err := r.CallWithAuth(context.Background(), "s", "read", json.RawMessage(`{}`)); !errors.As(err, &ar) || ar.MetadataURL == "" {
		t.Fatalf("discover hit should yield *AuthRequired with metadata, got %v", err)
	}

	// 钩子未命中：仍按普通错误
	if out, err := makeRouter(func(context.Context) (string, bool) { return "", false }).CallWithAuth(context.Background(), "s", "read", json.RawMessage(`{}`)); err != nil || !strings.Contains(out, "call s/read failed") {
		t.Fatalf("discover miss: out=%q err=%v", out, err)
	}
}

// bareAuthClient 始终返回裸 401（无资源元数据）。
type bareAuthClient struct{}

func (c *bareAuthClient) ListTools(context.Context) ([]ToolDef, error) { return nil, nil }
func (c *bareAuthClient) CallTool(context.Context, string, json.RawMessage) (string, error) {
	return "", &transport.AuthorizationRequiredError{}
}

// flakyAuthClient 首次调用报 OAuth 未授权，之后正常返回（确定性：不依赖 goroutine 时序）。
type flakyAuthClient struct{ calls int }

func (c *flakyAuthClient) ListTools(context.Context) ([]ToolDef, error) {
	return []ToolDef{{Name: "read"}}, nil
}

func (c *flakyAuthClient) CallTool(_ context.Context, name string, args json.RawMessage) (string, error) {
	c.calls++
	if c.calls == 1 {
		return "", &transport.OAuthAuthorizationRequiredError{Handler: transport.NewOAuthHandler(transport.OAuthConfig{})}
	}
	return fmt.Sprintf("%s(%s)", name, string(args)), nil
}

// mcp-go in-process 会话：真实协议栈的 list/call。
func TestSDKSession(t *testing.T) {
	srv := mcpserver.NewMCPServer("t", "1.0")
	srv.AddTool(mcp.NewTool("greet", mcp.WithDescription("打招呼")),
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return mcp.NewToolResultText("hi " + req.GetString("name", "")), nil
		})
	rawClient, err := mcpclient.NewInProcessClient(srv)
	if err != nil {
		t.Fatalf("in-process: %v", err)
	}

	client, err := handshake(rawClient)
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
