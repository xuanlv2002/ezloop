package types

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

type ToolResult struct {
	CallID  string
	Name    string
	Content string
	Err     error
}

type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // RoleTool 消息关联的调用 ID
	Err        string     `json:"err,omitempty"`          // 工具执行失败时的错误文本

	// Reasoning 是推理模型的思考过程（DeepSeek R1/O 系列的 reasoning_content），
	// 仅用于展示与持久化回放——协议要求请求不回传它（Provider 转换时丢弃）。
	Reasoning string `json:"reasoning,omitempty"`
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	// CachedTokens 是 prompt 命中服务端缓存的 token 数（OpenAI
	// prompt_tokens_details.cached_tokens / DeepSeek prompt_cache_hit_tokens）。
	CachedTokens int
}

func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.CachedTokens += other.CachedTokens
}

type StopReason string

const (
	StopCompleted    StopReason = "completed"
	StopMaxIteration StopReason = "max_iterations"
	StopAborted      StopReason = "aborted"
	StopError        StopReason = "error"
	StopCancelled    StopReason = "cancelled"
)
