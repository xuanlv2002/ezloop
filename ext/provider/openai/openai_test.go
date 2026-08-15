package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/types"
)

func newTestProvider(t *testing.T, handler http.HandlerFunc, headers map[string]string) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(Options{
		BaseURL: srv.URL,
		APIKey:  "sk-test",
		Model:   "gpt-test",
		Headers: headers,
	}), srv
}

func TestInvokeBasic(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("auth header: %q", got)
		}
		if got := r.Header.Get("X-Org"); got != "my-org" {
			t.Errorf("custom header missing: %q", got)
		}
		fmt.Fprint(w, `{
			"choices": [{"message": {"role": "assistant", "content": "你好"}}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5}
		}`)
	}, map[string]string{"X-Org": "my-org"})

	resp, err := p.Invoke(context.Background(), &types.ModelRequest{
		Messages: []types.Message{{Role: types.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Content != "你好" {
		t.Fatalf("content: %q", resp.Content)
	}
	if resp.Usage.PromptTokens != 10 || resp.Usage.CompletionTokens != 5 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

func TestInvokeRoundTripToolCalls(t *testing.T) {
	var received chatRequest
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		fmt.Fprint(w, `{
			"choices": [{"message": {"role": "assistant", "content": "",
				"tool_calls": [{"id": "call_1", "type": "function",
					"function": {"name": "echo", "arguments": "{\"msg\":\"a\"}"}}]}}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 2}
		}`)
	}, nil)

	resp, err := p.Invoke(context.Background(), &types.ModelRequest{
		Messages: []types.Message{
			{Role: types.RoleUser, Content: "hi"},
			{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{
				{ID: "call_0", Name: "echo", Args: json.RawMessage(`{"msg":"prev"}`)},
			}},
			{Role: types.RoleTool, ToolCallID: "call_0", Content: "echo: prev"},
			{Role: types.RoleTool, ToolCallID: "call_x", Err: "tool boom"},
		},
		Tools: []types.Tool{echoTool{}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// 请求侧：历史消息映射。
	if len(received.Messages) != 4 {
		t.Fatalf("messages: %d", len(received.Messages))
	}
	asst := received.Messages[1]
	if len(asst.ToolCalls) != 1 || asst.ToolCalls[0].Function.Arguments != `{"msg":"prev"}` {
		t.Fatalf("assistant tool_calls: %+v", asst.ToolCalls)
	}
	if received.Messages[3].Content != "error: tool boom" {
		t.Fatalf("tool err mapping: %q", received.Messages[3].Content)
	}
	if len(received.Tools) != 1 || received.Tools[0].Function.Name != "echo" {
		t.Fatalf("tools: %+v", received.Tools)
	}

	// 响应侧：tool call 映射。
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "echo" ||
		string(resp.ToolCalls[0].Args) != `{"msg":"a"}` {
		t.Fatalf("resp tool calls: %+v", resp.ToolCalls)
	}
}

func TestInvokeHTTPError(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error": {"message": "bad key"}}`)
	}, nil)

	_, err := p.Invoke(context.Background(), &types.ModelRequest{})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 error, got %v", err)
	}
}

func TestStreamAggregation(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"你"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"好"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"echo","arguments":"{\"msg\":"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3}}`+"\n\n")
		fmt.Fprint(w, `data: [DONE]`+"\n\n")
	}, nil)

	var chunks []string
	resp, err := p.Stream(context.Background(), &types.ModelRequest{}, func(c types.ModelChunk) error {
		chunks = append(chunks, c.ContentDelta)
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Content != "你好" {
		t.Fatalf("content: %q", resp.Content)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks: %v", chunks)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "call_1" ||
		resp.ToolCalls[0].Name != "echo" || string(resp.ToolCalls[0].Args) != `{"msg":"hi"}` {
		t.Fatalf("aggregated tool call: %+v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 7 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

type echoTool struct{}

func (echoTool) Name() string        { return "echo" }
func (echoTool) Description() string { return "echo" }
func (echoTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (echoTool) Invoke(_ context.Context, args json.RawMessage) (string, error) {
	return "echo: " + string(args), nil
}

// 与 core 引擎的集成：流式 provider + 工具回边。
func TestCoreIntegration(t *testing.T) {
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var req chatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		// 第一轮：请求工具；之后：直接回答。
		w.Header().Set("Content-Type", "text/event-stream")
		if len(req.Messages) <= 1 {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"echo","arguments":"{\"msg\":\"x\"}"}}]}}]}`+"\n\n")
		} else {
			fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"done"}}]}`+"\n\n")
		}
		fmt.Fprint(w, `data: [DONE]`+"\n\n")
	}, nil)

	agent := core.NewAgent(p, core.WithTools(echoTool{}), core.WithStreaming(true))
	state, err := agent.Run(context.Background(), "hi")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if state.StopReason != types.StopCompleted || state.Iteration != 2 {
		t.Fatalf("state: %s iter=%d", state.StopReason, state.Iteration)
	}
	if state.LastResponse.Content != "done" {
		t.Fatalf("final: %q", state.LastResponse.Content)
	}
}
