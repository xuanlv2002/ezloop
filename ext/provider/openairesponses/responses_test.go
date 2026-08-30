package openairesponses

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

// 转换矩阵：system 抽 instructions、user 图片转 parts、assistant 工具调用
// 独立 item、tool 结果转 function_call_output、Reasoning 不回传。
func TestToInput(t *testing.T) {
	msgs := []types.Message{
		{Role: types.RoleSystem, Content: "sys"},
		{Role: types.RoleUser, Content: "看图", Images: []types.ImagePart{{MimeType: "image/png", Data: "aGk="}}},
		{Role: types.RoleAssistant, Content: "hi", Reasoning: "chain"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "c1", Name: "echo", Args: []byte(`{"m":"a"}`)}}},
		{Role: types.RoleTool, ToolCallID: "c1", Content: "ok"},
	}
	instr, items := toInput(msgs)
	if instr != "sys" {
		t.Fatalf("instructions: %q", instr)
	}
	if len(items) != 4 {
		t.Fatalf("items: %+v", items)
	}
	// user 多模态：content 为 parts 数组（input_text + input_image data URI）
	if items[0].Content == "看图" {
		t.Fatalf("image message content should be parts: %+v", items[0])
	}
	parts, ok := items[0].Content.([]contentPart)
	if !ok || len(parts) != 2 || parts[1].ImageURL != "data:image/png;base64,aGk=" {
		t.Fatalf("parts: %+v", items[0].Content)
	}
	if items[1].Content != "hi" {
		t.Fatalf("assistant text: %+v", items[1])
	}
	if items[2].Type != "function_call" || items[2].CallID != "c1" || items[2].Name != "echo" || items[2].Arguments != `{"m":"a"}` {
		t.Fatalf("function_call item: %+v", items[2])
	}
	if items[3].Type != "function_call_output" || items[3].CallID != "c1" || items[3].Output != "ok" {
		t.Fatalf("function_call_output item: %+v", items[3])
	}
	if strings.Contains(fmt.Sprint(items), "chain") {
		t.Fatalf("reasoning must not leak: %+v", items)
	}
}

// 非流式：output 遍历（message/reasoning/function_call）与 usage 映射。
func TestInvokeParsesOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"output":[
			{"type":"reasoning","content":[{"type":"reasoning_text","text":"think"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer"}]},
			{"type":"function_call","call_id":"c1","name":"echo","arguments":"{\"m\":\"a\"}"}
		],"usage":{"input_tokens":100,"output_tokens":10,"input_tokens_details":{"cached_tokens":64}}}`)
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	resp, err := p.Invoke(context.Background(), &types.ModelRequest{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Content != "answer" || resp.Reasoning != "think" {
		t.Fatalf("resp: %+v", resp)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "c1" || string(resp.ToolCalls[0].Args) != `{"m":"a"}` {
		t.Fatalf("toolcalls: %+v", resp.ToolCalls)
	}
	if resp.Usage.PromptTokens != 100 || resp.Usage.CachedTokens != 64 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// 流式聚合：text/reasoning 增量透出、function_call 参数按 item_id 聚合、
// completed 取 usage。
func TestStreamAggregates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{
			`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc1","call_id":"c1","name":"echo"}}`,
			`data: {"type":"response.reasoning_text.delta","delta":"th"}`,
			`data: {"type":"response.output_text.delta","delta":"ans"}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"{\"m\":"}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"\"a\"}"}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":9,"output_tokens":3,"input_tokens_details":{"cached_tokens":5}}}}`,
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
	if resp.Usage.CachedTokens != 5 || resp.Usage.PromptTokens != 9 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// 兜底：call_id/name 只在 output_item.done 给出（部分端点 added 不带全）。
func TestStreamCallIDFromDone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{
			`data: {"type":"response.output_item.added","item":{"type":"function_call","id":"fc1"}}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc1","delta":"{}"}`,
			`data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc1","call_id":"c9","name":"late"}}`,
			`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
		} {
			fmt.Fprintln(w, s)
			fmt.Fprintln(w)
		}
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	resp, err := p.Stream(context.Background(), &types.ModelRequest{}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "c9" || resp.ToolCalls[0].Name != "late" {
		t.Fatalf("toolcalls: %+v", resp.ToolCalls)
	}
}
