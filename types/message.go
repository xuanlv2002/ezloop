package types

import "encoding/json"

/* Role 是消息角色。 */
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

/* ToolCall 是模型发起的一次工具调用请求。 */
type ToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

/* ToolResult 是一次工具调用的执行结果（toolEnd hook 可改写后入史）。 */
type ToolResult struct {
	CallID  string
	Name    string
	Content string
	Err     error
}

/* Message 是消息历史的原子单元，可序列化、可直接作为下一轮历史。 */
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // RoleTool 消息关联的调用 ID
	Err        string     `json:"err,omitempty"`          // 工具执行失败时的错误文本

	// 推理模型的思考过程（reasoning_content），仅展示与持久化回放——
	// 协议要求请求不回传，Provider 构造请求时丢弃。
	Reasoning string `json:"reasoning,omitempty"`
}

/* Usage 统计单次或整轮 loop 的 token 用量。 */
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	// prompt 命中服务端缓存的 token 数（OpenAI cached_tokens /
	// DeepSeek prompt_cache_hit_tokens 双协议取非零）。
	CachedTokens int
}

/* Add 累加另一份用量（多迭代、fork 分身汇总算回父循环）。 */
func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.CachedTokens += other.CachedTokens
}

/* StopReason 是 loop 终止原因。 */
type StopReason string

const (
	StopCompleted    StopReason = "completed"
	StopMaxIteration StopReason = "max_iterations"
	StopAborted      StopReason = "aborted"
	StopError        StopReason = "error"
	StopCancelled    StopReason = "cancelled"
)
