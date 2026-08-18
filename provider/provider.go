/* Package provider 抽象模型调用节点，换模型/多模型路由均通过实现本接口扩展。 */
package provider

import (
	"context"

	"github.com/xuanlv2002/ezloop/types"
)

/* ModelProvider 是非流式模型调用。 */
type ModelProvider interface {
	Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error)
}

/* ModelChunkHandler 接收流式增量；返回 error 可提前取消流。 */
type ModelChunkHandler func(chunk types.ModelChunk) error

/* StreamProvider 是流式模型调用，聚合出完整响应后返回。WithStreaming 启用时优先于 Invoke。 */
type StreamProvider interface {
	Stream(ctx context.Context, req *types.ModelRequest, onChunk ModelChunkHandler) (*types.ModelResponse, error)
}
