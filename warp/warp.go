// Package warp 定义节点装饰器：与 hook（横向切面，管节点前后）平级，
// warp 是纵向封装，管节点内部。两类节点的装饰器统一收在本包——
// 模型用 ModelHandler / Model，工具用 ToolHandler / Tool，
// 链式组装共用 Chain。
package warp

import (
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

// Handler 是通用节点装饰器：接收一个节点，返回增强后的节点。
// 类比 net/http 的 middleware 惯用法。
type Handler[T any] func(T) T

// Chain 用装饰器链包装 node：先注册的位于最外层，
// 即调用依次经过 h1 → h2 → ... → node。
func Chain[T any](node T, handlers ...Handler[T]) T {
	for i := len(handlers) - 1; i >= 0; i-- {
		node = handlers[i](node)
	}
	return node
}

// ModelHandler 是模型节点装饰器：重试、降级、限流、多模型路由等。
type ModelHandler = Handler[provider.ModelProvider]

// Model 用装饰器链包装模型节点：先注册的位于最外层。
func Model(p provider.ModelProvider, handlers ...ModelHandler) provider.ModelProvider {
	return Chain(p, handlers...)
}

// ToolHandler 是工具节点装饰器：防护、超时、审计、缓存等。
type ToolHandler = Handler[types.Tool]

// Tool 用装饰器链包装工具节点：先注册的位于最外层。
func Tool(t types.Tool, handlers ...ToolHandler) types.Tool {
	return Chain(t, handlers...)
}
