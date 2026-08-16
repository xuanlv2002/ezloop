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
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

func (u *Usage) Add(other Usage) {
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
}

type StopReason string

const (
	StopCompleted    StopReason = "completed"
	StopMaxIteration StopReason = "max_iterations"
	StopAborted      StopReason = "aborted"
	StopError        StopReason = "error"
	StopCancelled    StopReason = "cancelled"
)
