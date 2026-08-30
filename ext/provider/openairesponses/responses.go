/*
Package openairesponses 实现 OpenAI Responses 格式的 Provider
（POST {BaseURL}/responses），DeepSeek、OpenAI 等兼容端点替换
BaseURL 即可接入。API 无状态：每次请求回传完整 input item 列表
（消息 / function_call / function_call_output），system 走顶层
instructions。
*/
package openairesponses

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

const DefaultBaseURL = "https://api.deepseek.com"

/* DefaultTimeout 是单次请求（含流式全程）的默认超时。 */
const DefaultTimeout = 5 * time.Minute

type Options struct {
	// BaseURL 形如 https://api.deepseek.com，实际请求 {BaseURL}/responses。
	BaseURL string
	APIKey  string
	Model   string
	// Headers 追加到每个请求的自定义头（如组织 ID、代理网关要求的头）。
	Headers map[string]string
	// Client 可选，默认 http.DefaultClient。
	Client *http.Client
	// Timeout 单次请求（含流式全程读 body）的超时，默认 5 分钟。
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

type respRequest struct {
	Model        string      `json:"model"`
	Instructions string      `json:"instructions,omitempty"`
	Input        []inputItem `json:"input"`
	Tools        []respTool  `json:"tools,omitempty"`
	Stream       bool        `json:"stream,omitempty"`
}

type inputItem struct {
	Type string `json:"type"` // message | function_call | function_call_output

	Role    string `json:"role,omitempty"`    // message：user | assistant
	Content any    `json:"content,omitempty"` // message：string | []contentPart

	CallID    string `json:"call_id,omitempty"`   // function_call / function_call_output
	Name      string `json:"name,omitempty"`      // function_call
	Arguments string `json:"arguments,omitempty"` // function_call
	Output    string `json:"output,omitempty"`    // function_call_output
}

/* contentPart 是消息 content 数组的一个分块（input_text 或 input_image）。 */
type contentPart struct {
	Type string `json:"type"` // "input_text" | "input_image"
	Text string `json:"text,omitempty"`

	ImageURL string `json:"image_url,omitempty"` // data URI（内嵌 base64 图片）
}

type respTool struct {
	Type        string          `json:"type"` // "function"（扁平结构，非 function 嵌套）
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type outputItem struct {
	Type    string `json:"type"` // message | reasoning | function_call
	Role    string `json:"role,omitempty"`
	ID      string `json:"id,omitempty"` // 输出 item 标识（流式 delta 事件的 item_id）
	Content []struct {
		Type string `json:"type"` // output_text | reasoning_text
		Text string `json:"text"`
	} `json:"content,omitempty"`

	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type respUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details,omitempty"`
}

type respResponse struct {
	Output []outputItem `json:"output"`
	Usage  *respUsage   `json:"usage"`
}

/* toUsage 把协议用量映射到 types.Usage。 */
func toUsage(u *respUsage) types.Usage {
	if u == nil {
		return types.Usage{}
	}
	cached := 0
	if u.InputTokensDetails != nil {
		cached = u.InputTokensDetails.CachedTokens
	}
	return types.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		CachedTokens:     cached,
	}
}

/* 流式事件（data JSON 带 type 字段，按类型分派）。 */
type streamEvent struct {
	Type string `json:"type"`

	ItemID   string        `json:"item_id"` // delta 事件携带的输出 item 标识
	Item     outputItem    `json:"item"`    // output_item.added / done
	Delta    string        `json:"delta"`
	Response *respResponse `json:"response"` // completed / incomplete / failed
}

// ---- ezloop 类型与协议类型的双向映射 ----

/*
toInput 把消息历史转换为 Responses 输入：system 消息抽出为顶层
instructions（多条拼接）；assistant 文本与工具调用各自成 item；
tool 消息转 function_call_output；Reasoning 不回传（协议要求）。
user 多模态图片转 input_image 分块，纯文本保持 string。
*/
func toInput(msgs []types.Message) (string, []inputItem) {
	var sys []string
	items := make([]inputItem, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case types.RoleSystem:
			if m.Content != "" {
				sys = append(sys, m.Content)
			}
		case types.RoleUser:
			it := inputItem{Type: "message", Role: "user"}
			if len(m.Images) > 0 {
				parts := make([]contentPart, 0, len(m.Images)+1)
				if m.Content != "" {
					parts = append(parts, contentPart{Type: "input_text", Text: m.Content})
				}
				for _, img := range m.Images {
					parts = append(parts, contentPart{Type: "input_image",
						ImageURL: "data:" + img.MimeType + ";base64," + img.Data})
				}
				it.Content = parts
			} else {
				it.Content = m.Content
			}
			items = append(items, it)
		case types.RoleAssistant:
			if m.Content != "" {
				items = append(items, inputItem{Type: "message", Role: "assistant", Content: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := string(tc.Args)
				if len(args) == 0 {
					args = "{}"
				}
				items = append(items, inputItem{
					Type: "function_call", CallID: tc.ID, Name: tc.Name, Arguments: args,
				})
			}
		case types.RoleTool:
			out := m.Content
			if m.Err != "" {
				out = "error: " + m.Err
			}
			items = append(items, inputItem{
				Type: "function_call_output", CallID: m.ToolCallID, Output: out,
			})
		}
	}
	return strings.Join(sys, "\n\n"), items
}

func toRespTools(tools []types.Tool) []respTool {
	out := make([]respTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, respTool{
			Type:        "function",
			Name:        t.Name(),
			Description: t.Description(),
			Parameters:  t.ArgsSchema(),
		})
	}
	return out
}

/* fromOutput 遍历非流式响应的输出 item 列表组装 ModelResponse。 */
func fromOutput(out []outputItem, usage types.Usage) *types.ModelResponse {
	resp := &types.ModelResponse{Usage: usage}
	for _, it := range out {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				if c.Type == "output_text" {
					resp.Content += c.Text
				}
			}
		case "reasoning":
			for _, c := range it.Content {
				resp.Reasoning += c.Text
			}
		case "function_call":
			args := json.RawMessage(it.Arguments)
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			resp.ToolCalls = append(resp.ToolCalls, types.ToolCall{
				ID: it.CallID, Name: it.Name, Args: args,
			})
		}
	}
	return resp
}

// ---- 请求 ----

func (p *Provider) post(ctx context.Context, req *respRequest) (*http.Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.opts.BaseURL+"/responses", bytes.NewReader(body))
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

/* Invoke 非流式调用。 */
func (p *Provider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	instr, input := toInput(req.Messages)
	resp, err := p.post(ctx, &respRequest{
		Model: p.opts.Model, Instructions: instr, Input: input, Tools: toRespTools(req.Tools),
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := provutil.CheckStatus("responses", resp); err != nil {
		return nil, err
	}
	var out respResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("responses: decode response: %w", err)
	}
	return fromOutput(out.Output, toUsage(out.Usage)), nil
}

/*
Stream 流式调用：语义化 SSE 事件（data JSON 带 type 字段，无 [DONE]，
以 response.completed / incomplete / failed 收尾）。文本与思考增量经
onChunk 实时透出；function_call 参数按 item 聚合——call_id/name 以
output_item.added 为准，done 事件兜底回填（部分端点只在 done 给全）。
*/
func (p *Provider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.opts.Timeout)
	defer cancel()
	instr, input := toInput(req.Messages)
	resp, err := p.post(ctx, &respRequest{
		Model: p.opts.Model, Instructions: instr, Input: input,
		Tools: toRespTools(req.Tools), Stream: true,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := provutil.CheckStatus("responses", resp); err != nil {
		return nil, err
	}

	final := types.ModelResponse{}
	// function_call 聚合：item_id → 调用；order 保持注册序（delta 事件只带 item_id）。
	type accCall struct {
		id, name, args string
	}
	acc := map[string]*accCall{}
	var order []string

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
			return nil, fmt.Errorf("responses: decode event %q: %w", payload, err)
		}
		emit := func(c types.ModelChunk) error {
			if onChunk != nil {
				return onChunk(c)
			}
			return nil
		}
		switch ev.Type {
		case "response.output_item.added":
			if ev.Item.Type == "function_call" {
				id := ev.Item.ID
				if _, ok := acc[id]; !ok {
					acc[id] = &accCall{}
					order = append(order, id)
				}
				if ev.Item.CallID != "" {
					acc[id].id = ev.Item.CallID
				}
				if ev.Item.Name != "" {
					acc[id].name = ev.Item.Name
				}
			}
		case "response.output_text.delta":
			if ev.Delta != "" {
				final.Content += ev.Delta
				if err := emit(types.ModelChunk{ContentDelta: ev.Delta}); err != nil {
					return nil, err
				}
			}
		case "response.reasoning_text.delta":
			if ev.Delta != "" {
				final.Reasoning += ev.Delta
				if err := emit(types.ModelChunk{ReasoningDelta: ev.Delta}); err != nil {
					return nil, err
				}
			}
		case "response.function_call_arguments.delta":
			c := acc[ev.ItemID]
			if c == nil {
				c = &accCall{}
				acc[ev.ItemID] = c
				order = append(order, ev.ItemID)
			}
			c.args += ev.Delta
			if err := emit(types.ModelChunk{ToolCalls: []types.ToolCallDelta{{
				Index: len(order) - 1, ArgsDelta: ev.Delta,
			}}}); err != nil {
				return nil, err
			}
		case "response.output_item.done":
			if ev.Item.Type == "function_call" {
				c := acc[ev.Item.ID]
				if c == nil {
					c = &accCall{}
					acc[ev.Item.ID] = c
					order = append(order, ev.Item.ID)
				}
				if ev.Item.CallID != "" {
					c.id = ev.Item.CallID
				}
				if ev.Item.Name != "" {
					c.name = ev.Item.Name
				}
				if ev.Item.Arguments != "" && c.args == "" {
					c.args = ev.Item.Arguments
				}
			}
		case "response.completed", "response.incomplete", "response.failed":
			if ev.Response != nil {
				final.Usage = toUsage(ev.Response.Usage)
				// 部分端点只在最终事件给全 output（流中断线兜底）。
				if final.Content == "" && final.Reasoning == "" && len(acc) == 0 {
					r := fromOutput(ev.Response.Output, final.Usage)
					final = *r
				}
			}
			break stream
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("responses: read stream: %w", err)
	}
	for _, id := range order {
		c := acc[id]
		args := json.RawMessage(c.args)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		final.ToolCalls = append(final.ToolCalls, types.ToolCall{ID: c.id, Name: c.name, Args: args})
	}
	return &final, nil
}
