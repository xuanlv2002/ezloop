package event

import "context"

// 节点内部（provider / warp：重试、降级、卸载、防护）拿不到 LoopState，
// 事件出口经 ctx 携带：引擎在模型与工具两条 warp 链上平级注入，
// 节点内 EmitEvent 流经引擎的 OnEvent / RunAsync 通道。
//
// 并发契约：工具天然并发执行，本出口可能被多个 goroutine 同时调用——
// 回调必须并发安全且快速返回（RunAsync 的 channel 出口天然满足）。
type emitterKey struct{}

// ContextWithEmitter 将事件出口注入 ctx。
func ContextWithEmitter(ctx context.Context, emit func(Event)) context.Context {
	return context.WithValue(ctx, emitterKey{}, emit)
}

// EmitterFrom 取出 ctx 携带的事件出口；未注入时 ok=false。
func EmitterFrom(ctx context.Context) (func(Event), bool) {
	emit, ok := ctx.Value(emitterKey{}).(func(Event))
	return emit, ok
}

// EmitEvent 经 ctx 出口发送事件；未注入时为空操作（节点代码无需判空）。
// 建议事件类型加命名空间前缀，如 "modelretry.retry"。
func EmitEvent(ctx context.Context, typ EventType, data any) {
	if emit, ok := EmitterFrom(ctx); ok {
		emit(Event{Type: typ, Data: data})
	}
}
