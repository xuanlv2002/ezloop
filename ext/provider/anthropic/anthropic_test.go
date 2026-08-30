package anthropic

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

// 合并转换矩阵：system 抽顶层、相邻 user/tool 聚合单 turn、连续 assistant
// 合并、tool_use input object 化、user 图片转 image 块。
func TestToAnthropic(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: "sys"},
		{Role: types.RoleUser, Content: "看图", Images: []types.ImagePart{{MimeType: "image/png", Data: "aGk="}}},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "c1", Name: "echo", Args: []byte(`{"m":"a"}`)}}},
		{Role: types.RoleTool, ToolCallID: "c1", Content: "ok"},
		{Role: types.RoleTool, ToolCallID: "c2", Content: "", Err: "boom"},
		{Role: types.RoleAssistant, Content: "done"},
	}
	system, out := toAnthropic(msgs)
	if system != "sys" {
		t.Fatalf("system: %q", system)
	}
	// user(文本+图) → assistant(tool_use) → user(tool_result×2) → assistant(text)
	if len(out) != 4 {
		t.Fatalf("turns: %+v", out)
	}
	if out[0].Role != "user" || len(out[0].Content) != 2 ||
		out[0].Content[0].Type != "text" || out[0].Content[1].Type != "image" ||
		out[0].Content[1].Source.MediaType != "image/png" || out[0].Content[1].Source.Data != "aGk=" {
		t.Fatalf("user turn: %+v", out[0])
	}
	if out[1].Role != "assistant" || out[1].Content[0].Type != "tool_use" ||
		out[1].Content[0].ID != "c1" || string(out[1].Content[0].Input) != `{"m":"a"}` {
		t.Fatalf("tool_use: %+v", out[1])
	}
	if len(out[2].Content) != 2 ||
		out[2].Content[0].Type != "tool_result" || out[2].Content[0].ToolUseID != "c1" || out[2].Content[0].Content != "ok" ||
		out[2].Content[1].Content != "error: boom" {
		t.Fatalf("tool_result merge: %+v", out[2])
	}
	if out[3].Content[0].Text != "done" {
		t.Fatalf("final assistant: %+v", out[3])
	}
}

// 非法 Args 容错：包 {"_raw": "..."} 保持 input 为合法 object。
func TestAsObjectFallback(t *testing.T) {
	if got := string(asObject(nil)); got != "{}" {
		t.Fatalf("nil: %s", got)
	}
	if got := string(asObject([]byte(`{"a":1}`))); got != `{"a":1}` {
		t.Fatalf("valid: %s", got)
	}
	if got := string(asObject([]byte(`not json`))); !strings.HasPrefix(got, `{"_raw":`) {
		t.Fatalf("invalid: %s", got)
	}
	if got := string(asObject([]byte(`[1,2]`))); !strings.HasPrefix(got, `{"_raw":`) {
		t.Fatalf("array: %s", got)
	}
}

// 请求头与端点：x-api-key、anthropic-version、自定义 Headers 覆盖、
// max_tokens 默认值下发。
func TestHeadersAndEndpoint(t *testing.T) {
	var gotPath, gotKey, gotVer, gotCustom string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		gotCustom = r.Header.Get("x-org")
		body, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}]}`)
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "sk", Model: "m",
		Headers: map[string]string{"x-org": "ez", "anthropic-version": "2023-01-01"}})

	if _, err := p.Invoke(context.Background(), &types.ModelRequest{}); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if gotPath != "/v1/messages" || gotKey != "sk" || gotVer != "2023-01-01" || gotCustom != "ez" {
		t.Fatalf("path=%s key=%s ver=%s org=%s", gotPath, gotKey, gotVer, gotCustom)
	}
	if !strings.Contains(string(body), `"max_tokens":16384`) {
		t.Fatalf("max_tokens default: %s", body)
	}
}

// 非流式：content 块遍历（text/thinking/tool_use）与 usage 映射。
func TestInvokeParsesContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"content":[
			{"type":"thinking","thinking":"hmm"},
			{"type":"text","text":"answer"},
			{"type":"tool_use","id":"c1","name":"echo","input":{"m":"a"}}
		],"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":64}}`)
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	resp, err := p.Invoke(context.Background(), &types.ModelRequest{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Content != "answer" || resp.Reasoning != "hmm" {
		t.Fatalf("resp: %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "c1" || string(resp.ToolCalls[0].Args) != `{"m":"a"}` {
		t.Fatalf("toolcalls: %+v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CachedTokens != 64 || resp.Usage.CompletionTokens != 10 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// 流式全事件序列：text/thinking 增量、tool_use 的 input_json_delta 聚合、
// 用量两段拼接（message_start + message_delta）。
func TestStreamFullEventSequence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{
			`data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":32}}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ans"}}`,
			`data: {"type":"content_block_stop","index":0}`,
			`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
			`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"th"}}`,
			`data: {"type":"content_block_stop","index":1}`,
			`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"c1","name":"echo"}}`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"m\":"}}`,
			`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a\"}"}}`,
			`data: {"type":"content_block_stop","index":2}`,
			`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			`data: {"type":"message_stop"}`,
		} {
			fmt.Fprintln(w, s)
			fmt.Fprintln(w)
		}
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	var content, reasoning strings.Builder
	resp, err := p.Stream(context.Background(), &types.ModelRequest{}, func(c types.ModelChunk) error {
		content.WriteString(c.ContentDelta)
		reasoning.WriteString(c.ReasoningDelta)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Content != "ans" || resp.Reasoning != "th" {
		t.Fatalf("resp: %+v", resp)
	}
	if content.String() != "ans" || reasoning.String() != "th" {
		t.Fatalf("chunks: %q %q", content.String(), reasoning.String())
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "c1" || string(resp.ToolCalls[0].Args) != `{"m":"a"}` {
		t.Fatalf("toolcalls: %+v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CompletionTokens != 9 || resp.Usage.CachedTokens != 32 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}
