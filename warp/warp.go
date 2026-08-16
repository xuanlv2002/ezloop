// Package warp 定义节点装饰器的统一链式组装。
// 与 hook（横向切面，管节点前后）平级：warp 是纵向封装，管节点内部——
// model 与 tool 两类 Warp 共用本包的 Handler/Chain，消除平行实现。
package warp

// Handler 是节点装饰器：接收一个节点，返回增强后的节点。
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
