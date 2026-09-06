/*
Package anthropic 实现 Anthropic Messages 协议的 Provider
（POST {BaseURL}/v1/messages）。协议要点：system 走顶层参数、
max_tokens 必填、tool 结果以 user 角色 tool_result 块回传、相邻同
角色消息须合并——toAnthropic 的 emit 追加语义统一覆盖。
*/
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xuanlv2002/ezloop/ext/provider/provutil"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

const DefaultBaseURL = "https://api.anthropic.com"

/* DefaultTimeout 是单次请求（含流式全程）的默认超时。 */
const DefaultTimeout = 5 * time.Minute

/* DefaultMaxTokens 是 max_tokens 必填字段的默认值（协议要求）。 */
const DefaultMaxTokens = 16384

/* APIVersion 是 anthropic-version 头（自定义 Headers 可覆盖）。 */
const APIVersion = "2023-06-01"

type Options struct {
	// BaseURL 形如 https://api.anthropic.com，实际请求 {BaseURL}/v1/messages。
	BaseURL string
	APIKey  string
	Model   string
	// Headers 追加到每个请求的自定义头（最后设置，可覆盖 anthropic-version 等）。
	Headers map[string]string
	// Client 可选，默认 http.DefaultClient。
	Client *http.Client
	// Timeout 单次请求（含流式全程读 body）的超时，默认 5 分钟。
	Timeout time.Duration
	// MaxTokens 单次响应生成上限（协议必填），默认 16384。
	MaxTokens int
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
	if opts.MaxTokens <= 0 {
		opts.MaxTokens = DefaultMaxTokens
	}
	client := opts.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &Provider{opts: opts, client: client}
}

// ---- 协议结构 ----

type antRequest struct {
	Model     string       `json:"model"`
	System    string       `json:"system,omitempty"`
	MaxTokens int          `json:"max_tokens"`
	Messages  []antMessage `json:"messages"`
	Tools     []antTool    `json:"tools,omitempty"`
	Stream    bool         `json:"stream,omitempty"`
}

type antMessage struct {
	Role    string     `json:"role"` // user | assistant
	Content []antBlock `json:"content"`
}

/* antBlock 是 content 的一个分块；请求侧填 text/image/tool_use/tool_result，响应侧多 thinking。 */
type antBlock struct {
	Type string `json:"type"` // text | image | tool_use | tool_result | thinking

	Text     string `json:"text,omitempty"`     // text
	Thinking string `json:"thinking,omitempty"` // thinking（响应侧）

	Source *antImageSource `json:"source,omitempty"` // image

	ID    string          `json:"id,omitempty"`    // tool_use
	Name  string          `json:"name,omitempty"`  // tool_use
	Input json.RawMessage `json:"input,omitempty"` // tool_use（object）

	ToolUseID string `json:"tool_use_id,omitempty"` // tool_result
	Content   string `json:"content,omitempty"`     // tool_result 结果文本
}

type antImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type antUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type antResponse struct {
	Content []antBlock `json:"content"`
	Usage   *antUsage  `json:"usage"`
}

/* toUsage 把协议用量映射到 types.Usage。anthropic 的 input_tokens 不含
缓存命中/写入部分，OpenAI 语义的 prompt_tokens 含缓存（cached ⊆ prompt，
水位与命中率口径）——此处按后者对齐。 */
func toUsage(u *antUsage) types.Usage {
	if u == nil {
		return types.Usage{}
	}
	return types.Usage{
		PromptTokens:     u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		CompletionTokens: u.OutputTokens,
		CachedTokens:     u.CacheReadInputTokens,
	}
}

/* 流式事件（data JSON 带 type 字段）。 */
type streamEvent struct {
	Type string `json:"type"`

	Message      *antResponse `json:"message"`       // message_start
	Index        int          `json:"index"`         // content_block_*
	ContentBlock *antBlock    `json:"content_block"` // content_block_start
	Delta        *antDelta    `json:"delta"`         // content_block_delta / message_delta
	Usage        *antUsage    `json:"usage"`         // message_delta 携带的最终用量
}

type antDelta struct {
	Type string `json:"type"` // text_delta | thinking_delta | input_json_delta

	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
}

// ---- ezloop 类型与协议类型的双向映射 ----

/*
asObject 保证 tool_use.input 是合法 JSON object：模型生成的 Args 几乎
总为有效 object，非法时包一层 {"_raw": "..."} 保留信息（协议要求 object）。
*/
func asObject(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil && m != nil {
		return raw
	}
	esc, _ := json.Marshal(string(raw))
	return json.RawMessage(`{"_raw":` + string(esc) + `}`)
}

/*
toAnthropic 把消息历史转换为协议消息列表：system 抽顶层；tool 消息转
user 角色 tool_result 块。emit 的"同角色追加"天然满足协议约束——相邻
user/tool 聚合单 turn、连续 assistant 合并、tool_result 紧跟 tool_use
所在的 assistant（历史顺序保证配对相邻）。Reasoning 不回传。
*/
func toAnthropic(msgs []types.Message) (string, []antMessage) {
	var sys []string
	out := make([]antMessage, 0, len(msgs))
	emit := func(role string, b antBlock) {
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, b)
			return
		}
		out = append(out, antMessage{Role: role, Content: []antBlock{b}})
	}
	for _, m := range msgs {
		switch m.Role {
		case types.RoleSystem:
			if m.Content != "" {
				sys = append(sys, m.Content)
			}
		case types.RoleUser:
			if m.Content != "" {
				emit("user", antBlock{Type: "text", Text: m.Content})
			}
			for _, img := range m.Images {
				emit("user", antBlock{Type: "image", Source: &antImageSource{
					Type: "base64", MediaType: img.MimeType, Data: img.Data,
				}})
			}
		case types.RoleAssistant:
			if m.Content != "" {
				emit("assistant", antBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				emit("assistant", antBlock{Type: "tool_use",
					ID: tc.ID, Name: tc.Name, Input: asObject(tc.Args)})
			}
		case types.RoleTool:
			content := m.Content
			if m.Err != "" {
				content = "error: " + m.Err
			}
			emit("user", antBlock{Type: "tool_result", ToolUseID: m.ToolCallID, Content: content})
		}
	}
	return strings.Join(sys, "\n\n"), out
}

func toAntTools(tools []types.Tool) []antTool {
	out := make([]antTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, antTool{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: t.ArgsSchema(),
		})
	}
	return out
}

/* fromContent 遍历响应 content 块组装 ModelResponse（非流式）。 */
func fromContent(blocks []antBlock, usage types.Usage) *types.ModelResponse {
	resp := &types.ModelResponse{Usage: usage}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			resp.Content += b.Text
		case "thinking":
			resp.Reasoning += b.Thinking
		case "tool_use":
			args := b.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			resp.ToolCalls = append(resp.ToolCalls, types.ToolCall{
				ID: b.ID, Name: b.Name, Args: args,
			})
		}
	}
	return resp
}

// ---- 请求 ----

func (p *Provider) post(ctx context.Context, req *antRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.opts.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.opts.APIKey)
	httpReq.Header.Set("anthropic-version", APIVersion)
	for k, v := range p.opts.Headers {
		httpReq.Header.Set(k, v)
	}
	return p.client.Do(httpReq)
}

/* Invoke 非流式调用。 */
func (p *Provider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	system, messages := toAnthropic(req.Messages)
	resp, err := p.post(ctx, &antRequest{
		Model: p.opts.Model, System: system, MaxTokens: p.opts.MaxTokens,
		Messages: messages, Tools: toAntTools(req.Tools),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := provutil.CheckStatus("anthropic", resp); err != nil {
		return nil, err
	}
	var out antResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	return fromContent(out.Content, toUsage(out.Usage)), nil
}

/*
Stream 流式调用：text/thinking 增量经 onChunk 实时透出；tool_use 的
input 由 input_json_delta 按 content block index 聚合（index 含 text/
thinking 块，tool 下标按 tool_use 出现顺序另行递增分配），名称在
content_block_start 即以 NameDelta 透出（对齐 openai 协议的流式契约：
消费者凭流式增量即可拿到工具名）。用量来自
message_start（input 侧）与 message_delta（output 侧）两段。
*/
func (p *Provider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	system, messages := toAnthropic(req.Messages)
	resp, err := p.post(ctx, &antRequest{
		Model: p.opts.Model, System: system, MaxTokens: p.opts.MaxTokens,
		Messages: messages, Tools: toAntTools(req.Tools), Stream: true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := provutil.CheckStatus("anthropic", resp); err != nil {
		return nil, err
	}

	final := types.ModelResponse{}
	usage := antUsage{}
	type accCall struct {
		id, name, args string
	}
	acc := map[int]*accCall{}       // content block index → 调用
	blockIdxToTool := map[int]int{} // content block index → tool 下标
	var toolSeq int

	emit := func(c types.ModelChunk) error {
		if onChunk != nil {
			return onChunk(c)
		}
		return nil
	}

	scanner := provutil.SSEScanner(resp.Body)
stream:
	for scanner.Scan() {
		line := scanner.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if len(payload) == 0 {
			continue
		}
		var ev streamEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return nil, fmt.Errorf("anthropic: decode event %q: %w", payload, err)
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil && ev.Message.Usage != nil {
				usage.InputTokens = ev.Message.Usage.InputTokens
				usage.CacheReadInputTokens = ev.Message.Usage.CacheReadInputTokens
				usage.CacheCreationInputTokens = ev.Message.Usage.CacheCreationInputTokens
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				acc[ev.Index] = &accCall{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
				blockIdxToTool[ev.Index] = toolSeq
				toolSeq++
				if err := emit(types.ModelChunk{ToolCalls: []types.ToolCallDelta{{
					Index:     blockIdxToTool[ev.Index],
					NameDelta: ev.ContentBlock.Name,
				}}}); err != nil {
					return nil, err
				}
			}
		case "content_block_delta":
			d := ev.Delta
			if d == nil {
				continue
			}
			switch d.Type {
			case "text_delta":
				final.Content += d.Text
				if err := emit(types.ModelChunk{ContentDelta: d.Text}); err != nil {
					return nil, err
				}
			case "thinking_delta":
				final.Reasoning += d.Thinking
				if err := emit(types.ModelChunk{ReasoningDelta: d.Thinking}); err != nil {
					return nil, err
				}
			case "input_json_delta":
				c := acc[ev.Index]
				if c == nil {
					c = &accCall{}
					acc[ev.Index] = c
					blockIdxToTool[ev.Index] = toolSeq
					toolSeq++
				}
				c.args += d.PartialJSON
				if err := emit(types.ModelChunk{ToolCalls: []types.ToolCallDelta{{
					Index: blockIdxToTool[ev.Index], ArgsDelta: d.PartialJSON,
				}}}); err != nil {
					return nil, err
				}
			}
		case "message_delta":
			if ev.Usage != nil {
				usage.OutputTokens = ev.Usage.OutputTokens
				// 新版协议与部分网关只在 message_delta 携带完整 usage——
				// 非零才覆盖（官方旧行为该处只有 output_tokens）
				if ev.Usage.InputTokens > 0 {
					usage.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
			}
		case "message_stop":
			break stream
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("anthropic: read stream: %w", err)
	}

	final.Usage = toUsage(&usage)
	for i := 0; i < toolSeq; i++ {
		var c *accCall
		for idx, t := range blockIdxToTool {
			if t == i {
				c = acc[idx]
				break
			}
		}
		if c == nil {
			continue
		}
		args := json.RawMessage(c.args)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		final.ToolCalls = append(final.ToolCalls, types.ToolCall{ID: c.id, Name: c.name, Args: args})
	}
	return &final, nil
}
