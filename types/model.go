package types

/* ModelRequest 是引擎调用 Provider 时的完整输入。 */
type ModelRequest struct {
	Messages []Message
	Tools    []Tool
}

/* ModelResponse 是模型单次响应：纯文本，或附带 tool calls 触发下一轮循环。 */
type ModelResponse struct {
	Content   string
	ToolCalls []ToolCall
	Usage     Usage
	// 推理模型的思考过程，入史到 Message.Reasoning（不回传 Provider）。
	Reasoning string
}

/*
ToolCallDelta 是流式工具调用的增量分片：Index 定位同轮第几个调用
（协议侧的累积序号），各字段是本片新增量——ID 一次性给出，
名字与参数按序拼接。消费方按 Index 分桶累积。
*/
type ToolCallDelta struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	NameDelta string `json:"nameDelta,omitempty"`
	ArgsDelta string `json:"argsDelta,omitempty"`
}

/* ModelChunk 是流式输出的增量：正文与思考过程分开通出，工具调用增量按片透出。 */
type ModelChunk struct {
	ContentDelta   string
	ReasoningDelta string
	ToolCalls      []ToolCallDelta
}
