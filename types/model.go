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
	// Reasoning 是推理模型的思考过程，入史到 Message.Reasoning（不回传 Provider）。
	Reasoning string
}

// ModelChunk 是流式输出的增量：正文与思考过程分开通出。
type ModelChunk struct {
	ContentDelta   string
	ReasoningDelta string
}
