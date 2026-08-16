// warp.go 定义模型中间件：引擎标准能力，
// 用户可提供 Warp 对模型调用做重试、降级、日志、限流、多模型路由等操作。
// 链式组装与 types.ToolWarp 共用框架层 warp.Chain。
package provider

import "github.com/xuanlv2002/ezloop/warp"

// WarpHandler 是模型中间件：接收一个 ModelProvider，返回增强后的实现。
// 类比 net/http 的 middleware 惯用法。
type WarpHandler = warp.Handler[ModelProvider]

// Warp 用中间件链包装 p：先注册的位于最外层，
// 即请求依次经过 warp1 → warp2 → ... → p。
func Warp(p ModelProvider, warps ...WarpHandler) ModelProvider {
	return warp.Chain(p, warps...)
}
