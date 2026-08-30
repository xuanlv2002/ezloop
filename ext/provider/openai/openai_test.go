package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// toUsage 兼容两套缓存字段:OpenAI prompt_tokens_details 与 DeepSeek prompt_cache_hit_tokens。
func TestToUsageCachedTokens(t *testing.T) {
	openaiStyle := &chatUsage{
		PromptTokens:     100,
		CompletionTokens: 10,
		PromptTokensDetails: &struct {
			CachedTokens int `json:"cached_tokens"`
		}{CachedTokens: 64},
	}
	if u := toUsage(openaiStyle); u.CachedTokens != 64 {
		t.Fatalf("openai style cached: %+v", u)
	}
	deepseekStyle := &chatUsage{PromptTokens: 100, PromptCacheHitTokens: 32}
	if u := toUsage(deepseekStyle); u.CachedTokens != 32 {
		t.Fatalf("deepseek style cached: %+v", u)
	}
	if u := toUsage(nil); u != (types.Usage{}) {
		t.Fatalf("nil usage: %+v", u)
	}
}

// 非流式:reasoning_content 进 ModelResponse.Reasoning,缓存 token 进 Usage。
func TestInvokeParsesReasoningAndCachedUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"answer","reasoning_content":"thinking"}}],"usage":{"prompt_tokens":100,"completion_tokens":10,"prompt_cache_hit_tokens":64}}`)
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	resp, err := p.Invoke(context.Background(), &types.ModelRequest{})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Content != "answer" || resp.Reasoning != "thinking" {
		t.Fatalf("resp: %+v", resp)
	}
	if resp.Usage.CachedTokens != 64 || resp.Usage.PromptTokens != 100 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// 流式:reasoning 增量聚合并经 onChunk 透出,与 content 增量分开通出。
func TestStreamAggregatesReasoning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, s := range []string{
			`data: {"choices":[{"delta":{"reasoning_content":"th"}}]}`,
			`data: {"choices":[{"delta":{"reasoning_content":"ink"}}]}`,
			`data: {"choices":[{"delta":{"content":"ans"}}]}`,
			`data: {"usage":{"prompt_tokens":5,"prompt_cache_hit_tokens":3}}`,
			`data: [DONE]`,
		} {
			fmt.Fprintln(w, s)
			fmt.Fprintln(w)
		}
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	var reasoning, content strings.Builder
	resp, err := p.Stream(context.Background(), &types.ModelRequest{}, func(c types.ModelChunk) error {
		reasoning.WriteString(c.ReasoningDelta)
		content.WriteString(c.ContentDelta)
		return nil
	})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Reasoning != "think" || resp.Content != "ans" {
		t.Fatalf("resp: reasoning=%q content=%q", resp.Reasoning, resp.Content)
	}
	if reasoning.String() != "think" || content.String() != "ans" {
		t.Fatalf("chunks: reasoning=%q content=%q", reasoning.String(), content.String())
	}
	if resp.Usage.CachedTokens != 3 {
		t.Fatalf("usage: %+v", resp.Usage)
	}
}

// 请求不回传 reasoning:历史里带 Reasoning 的 assistant 消息序列化后不含该字段。
func TestRequestDropsReasoning(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	req := &types.ModelRequest{Messages: []types.Message{
		{Role: types.RoleUser, Content: "q"},
		{Role: types.RoleAssistant, Content: "a", Reasoning: "secret chain"},
	}}
	if _, err := p.Invoke(context.Background(), req); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	body, _ := json.Marshal(got.Messages)
	if strings.Contains(string(body), "reasoning") {
		t.Fatalf("request must not carry reasoning: %s", body)
	}
}

var _ provider.ModelProvider = (*Provider)(nil)

// 多模态序列化：有图 user 消息 content 为 parts 数组（text+image_url data URI），
// 无图消息保持纯 string（请求字节兼容），tool 消息忽略图片。
func TestMultimodalContentSerialization(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()
	p := New(Options{BaseURL: srv.URL, APIKey: "x", Model: "m"})

	req := &types.ModelRequest{Messages: []types.Message{
		{Role: types.RoleUser, Content: "看图", Images: []types.ImagePart{
			{MimeType: "image/png", Data: "aGk="},
		}},
		{Role: types.RoleAssistant, Content: "a"},
		{Role: types.RoleUser, Content: "纯文本"},
	}}
	if _, err := p.Invoke(context.Background(), req); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := `"messages":[{"role":"user","content":[{"type":"text","text":"看图"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,aGk="}}]},` +
		`{"role":"assistant","content":"a"},{"role":"user","content":"纯文本"}]`
	if !strings.Contains(string(body), want) {
		t.Fatalf("body: %s", body)
	}
}
