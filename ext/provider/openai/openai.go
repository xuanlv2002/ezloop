// Package openai 实现 OpenAI 兼容协议的 Provider，
// 任何兼容 /chat/completions 的端点（OpenAI/DeepSeek/vLLM/Ollama 等）
// 只需替换 BaseURL 即可接入。
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

const DefaultBaseURL = "https://api.openai.com/v1"

// DefaultTimeout 是单次请求（含流式全程）的默认超时。
const DefaultTimeout = 5 * time.Minute

type Options struct {
	// BaseURL 形如 https://api.openai.com/v1，实际请求 {BaseURL}/chat/completions。
	BaseURL string
	APIKey  string
	Model   string
	// Headers 追加到每个请求的自定义头（如组织 ID、代理网关要求的头）。
	Headers map[string]string
	// Client 可选，默认 http.DefaultClient。
	Client *http.Client
	// Timeout 单次请求（含流式全程读 body）的超时，默认 5 分钟。
	// 与调用方 ctx 的 deadline 取更早者；需要更长的流式任务请显式调大。
	Timeout time.Duration
}

type Provider struct {
	opts   Options
	client *http.Client
}

var _ provider.ModelProvider = (*Provider)(nil)
var _ provider.StreamProvider = (*Provider)(nil)

func New(opts Options) *Provider {
	if opts.BaseURL == "" {
		opts.BaseURL = DefaultBaseURL
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{opts: opts, client: client}
}

// ---- 协议结构 ----

type chatRequest struct {
	Model         string         `json:"model"`
	Messages      []chatMessage  `json:"messages"`
	Tools         []chatTool     `json:"tools,omitempty"`
	Stream        bool           `json:"stream,omitempty"`
	StreamOptions *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`

	// ReasoningContent 是推理模型（DeepSeek R1 / OpenAI o 系列兼容端点）返回的
	// 思考过程，仅出现在响应侧；toChatMessages 从 types.Message 构造请求时
	// 不填它（协议要求请求不回传 reasoning）。
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Index    int              `json:"index"`
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// 缓存命中两套字段：OpenAI prompt_tokens_details.cached_tokens、
	// DeepSeek prompt_cache_hit_tokens，取非零者。
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details,omitempty"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
}

// toUsage 把协议用量映射到 types.Usage。
func toUsage(u *chatUsage) types.Usage {
	if u == nil {
		return types.Usage{}
	}
	cached := u.PromptCacheHitTokens
	if cached == 0 && u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	}
	return types.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		CachedTokens:     cached,
	}
}

type streamChunk struct {
	Choices []struct {
		Delta chatMessage `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// ---- ezloop 类型与协议类型的双向映射 ----

func toChatMessages(msgs []types.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, m := range msgs {
		cm := chatMessage{Role: string(m.Role), Content: m.Content}
		if m.Role == types.RoleTool {
			cm.ToolCallID = m.ToolCallID
			if m.Err != "" {
				cm.Content = "error: " + m.Err
			}
		}
		for _, tc := range m.ToolCalls {
			args := string(tc.Args)
			if len(args) == 0 {
				args = "{}"
			}
			cm.ToolCalls = append(cm.ToolCalls, chatToolCall{
				ID: tc.ID, Type: "function",
				Function: chatFunctionCall{Name: tc.Name, Arguments: args},
			})
		}
		out = append(out, cm)
	}
	return out
}

func toChatTools(tools []types.Tool) []chatTool {
	out := make([]chatTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  t.ArgsSchema(),
			},
		})
	}
	return out
}

func fromChatMessage(msg chatMessage, usage types.Usage) *types.ModelResponse {
	resp := &types.ModelResponse{Content: msg.Content, Reasoning: msg.ReasoningContent, Usage: usage}
	for _, tc := range msg.ToolCalls {
		args := json.RawMessage(tc.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		resp.ToolCalls = append(resp.ToolCalls, types.ToolCall{
			ID:   tc.ID,
			Name: tc.Function.Name,
			Args: args,
		})
	}
	return resp
}

// ---- 请求 ----

func (p *Provider) post(ctx context.Context, req *chatRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.opts.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.opts.APIKey)
	for k, v := range p.opts.Headers {
		httpReq.Header.Set(k, v)
	}
	return p.client.Do(httpReq)
}

// HTTPError 是非 2xx 响应的结构化错误，实现 Retryable() bool：
// modelretry 等装饰器按此判断可重试性，无需解析错误文本。
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("openai: http %d: %s", e.Status, e.Body)
}

// Retryable：408（请求超时）、429（限流）与 5xx 可安全重试；
// 其余 4xx（鉴权错误、请求格式错误等）重试无意义。
func (e *HTTPError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests ||
		e.Status >= http.StatusInternalServerError
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return &HTTPError{Status: resp.StatusCode, Body: string(body)}
	}
	return nil
}

// Invoke 非流式调用。
func (p *Provider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	// 超时包住整个调用（含读 body）；不用 http.Client.Timeout 是因为它对
	// 流式不友好，这里 Invoke / Stream 统一用 context 控制。
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	chatReq := &chatRequest{
		Model:    p.opts.Model,
		Messages: toChatMessages(req.Messages),
		Tools:    toChatTools(req.Tools),
	}
	resp, err := p.post(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}

	var out chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("openai: empty choices")
	}
	usage := toUsage(out.Usage)
	return fromChatMessage(out.Choices[0].Message, usage), nil
}

// Stream 流式调用：content 增量经 onChunk 实时透出，
// tool call 参数分片在内部聚合，最终返回完整响应。
func (p *Provider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	// 超时覆盖流式全程（建连 + SSE 读到 [DONE]），防服务端挂起拖死 loop。
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	chatReq := &chatRequest{
		Model:         p.opts.Model,
		Messages:      toChatMessages(req.Messages),
		Tools:         toChatTools(req.Tools),
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
	}
	resp, err := p.post(ctx, chatReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}

	final := types.ModelResponse{}
	// tool call 增量按 index 聚合。
	type accCall struct {
		id, name, args string
	}
	acc := map[int]*accCall{}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if string(payload) == "[DONE]" {
			break
		}
		var chunk streamChunk
		if err := json.Unmarshal(payload, &chunk); err != nil {
			return nil, fmt.Errorf("openai: decode chunk %q: %w", payload, err)
		}
		if chunk.Usage != nil {
			final.Usage = toUsage(chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.ReasoningContent != "" {
			final.Reasoning += delta.ReasoningContent
			if onChunk != nil {
				if err := onChunk(types.ModelChunk{ReasoningDelta: delta.ReasoningContent}); err != nil {
					return nil, err
				}
			}
		}
		if delta.Content != "" {
			final.Content += delta.Content
			if onChunk != nil {
				if err := onChunk(types.ModelChunk{ContentDelta: delta.Content}); err != nil {
					return nil, err
				}
			}
		}
		for _, tc := range delta.ToolCalls {
			c, ok := acc[tc.Index]
			if !ok {
				c = &accCall{}
				acc[tc.Index] = c
			}
			if tc.ID != "" {
				c.id = tc.ID
			}
			if tc.Function.Name != "" {
				c.name += tc.Function.Name
			}
			c.args += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("openai: read stream: %w", err)
	}

	for i := 0; i < len(acc); i++ {
		c := acc[i]
		args := json.RawMessage(c.args)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		final.ToolCalls = append(final.ToolCalls, types.ToolCall{
			ID: c.id, Name: c.name, Args: args,
		})
	}
	return &final, nil
}
