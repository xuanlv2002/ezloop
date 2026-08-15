package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

// mockClient 模拟一个 MCP server。
type mockClient struct {
	tools  []ToolDef
	closed bool
}

func (c *mockClient) ListTools(_ context.Context) ([]ToolDef, error) { return c.tools, nil }

func (c *mockClient) CallTool(_ context.Context, name string, args json.RawMessage) (string, error) {
	if name == "boom" {
		return "", fmt.Errorf("internal failure")
	}
	return fmt.Sprintf("%s(%s)", name, string(args)), nil
}

func (c *mockClient) Close() error { c.closed = true; return nil }

func testCfg() Config {
	return Config{Servers: []ServerConfig{
		{
			Name:  "db",
			Allow: []string{"query"},
			Factory: func(ServerConfig) (Client, error) {
				return &mockClient{tools: []ToolDef{
					{Name: "query", Description: "run sql", ArgsSchema: json.RawMessage(`{"type":"object"}`)},
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

func callRouter(t *testing.T, r *Router, args string) string {
	t.Helper()
	out, err := r.Invoke(context.Background(), json.RawMessage(args))
	if err != nil {
		t.Fatalf("invoke err: %v", err)
	}
	return out
}

func TestRouterListTools(t *testing.T) {
	r := NewRouter(testCfg().Servers)
	out := callRouter(t, r, `{"action":"list_tools"}`)
	var entries []toolEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	// db 只暴露白名单里的 query；fs 暴露 read。
	got := map[string]bool{}
	for _, e := range entries {
		got[e.Server+"/"+e.Name] = true
	}
	if !got["db/query"] || !got["fs/read"] {
		t.Fatalf("expected tools missing: %v", got)
	}
	if got["db/drop"] {
		t.Fatal("db/drop should be filtered by ACL")
	}
}

func TestRouterCallTool(t *testing.T) {
	r := NewRouter(testCfg().Servers)
	out := callRouter(t, r, `{"action":"call_tool","server":"fs","tool":"read","args":{"path":"a.txt"}}`)
	if out != `read({"path":"a.txt"})` {
		t.Fatalf("bad result: %s", out)
	}
}

func TestRouterACLDenied(t *testing.T) {
	r := NewRouter(testCfg().Servers)
	out := callRouter(t, r, `{"action":"call_tool","server":"db","tool":"drop"}`)
	if !json.Valid([]byte(out)) {
		t.Fatalf("want structured error json, got %s", out)
	}
	var ce callError
	_ = json.Unmarshal([]byte(out), &ce)
	if ce.Error == "" {
		t.Fatalf("want error field, got %s", out)
	}
}

func TestRouterCallErrorIsStructured(t *testing.T) {
	r := NewRouter(testCfg().Servers)
	out := callRouter(t, r, `{"action":"call_tool","server":"fs","tool":"boom"}`)
	var ce callError
	if err := json.Unmarshal([]byte(out), &ce); err != nil {
		t.Fatalf("want structured error json, got %s", out)
	}
	if ce.Hint == "" {
		t.Fatal("want self-correction hint")
	}
}

func TestRouterUnknownServer(t *testing.T) {
	r := NewRouter(testCfg().Servers)
	out := callRouter(t, r, `{"action":"call_tool","server":"nope","tool":"x"}`)
	var ce callError
	_ = json.Unmarshal([]byte(out), &ce)
	if ce.Error == "" {
		t.Fatalf("want structured error, got %s", out)
	}
}

func TestRouterReplaceServersClosesClients(t *testing.T) {
	r := NewRouter(testCfg().Servers)
	// 建立连接。
	_ = callRouter(t, r, `{"action":"list_tools"}`)

	newCfg := Config{Servers: []ServerConfig{{
		Name: "only",
		Factory: func(ServerConfig) (Client, error) {
			return &mockClient{}, nil
		},
	}}}
	r.ReplaceServers(newCfg.Servers)

	out := callRouter(t, r, `{"action":"list_tools"}`)
	if !json.Valid([]byte(out)) {
		t.Fatalf("bad json: %s", out)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close err: %v", err)
	}
}
