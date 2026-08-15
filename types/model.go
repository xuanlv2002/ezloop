package types

// ModelRequest 是引擎调用 Provider 时的完整输入。
type ModelRequest struct {
	Messages []Message
	Tools    []Tool
}

// ModelResponse 是模型单次响应：纯文本，或附带 tool calls 触发下一轮循环。
type ModelResponse struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
}

// ModelChunk 是流式输出的增量文本。
type ModelChunk struct {
	ContentDelta string
}
