# Warp 装饰器

warp 是节点中间件：包住节点本身，决定**节点怎么执行**。模型调用和工具执行是两类节点，各有对应的装饰器挂载点。与 hook 的分工见[设计理念](设计理念.md)——warp 在节点内，hook 在节点外。

## 统一签名

```go
type Handler[T any] func(em event.Emitter, node T) T

type ModelHandler = Handler[provider.ModelProvider] // WithModelWarp
type ToolHandler  = Handler[types.Tool]             // WithToolWarp
```

装饰器就是「接收一个节点，返回一个包了内层的同名节点」。工厂拿到 `event.Emitter`（观察出口）——可以发事件，碰不到 `LoopState`；需要状态的是 hook，不是 warp。

## 模型装饰器

重试是模型侧最常见的需求（429 / 5xx 指数退避；流式仅在尚未发出 chunk 时可重试，发出过就重放不了）。`ext/warp/model/modelretry` 有一个参考实现，也可以照下面的自定义写法自己写一个。

自定义一个记时装饰器——内嵌接口、覆盖 Invoke，是最省事的写法：

```go
type logModel struct{ provider.ModelProvider }

func (m logModel) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
    start := time.Now()
    resp, err := m.ModelProvider.Invoke(ctx, req)
    log.Printf("model call %v err=%v", time.Since(start), err)
    return resp, err
}

core.WithModelWarp(func(_ event.Emitter, p provider.ModelProvider) provider.ModelProvider {
    return logModel{p}
})
```

未覆盖的方法（如 `Stream`）自动透传给内层，链上每一层都是这样。

## 工具装饰器

工具侧最常见的两个：并发闸（一轮 fan-out 限到 4 个）与 panic 防护——防护必须包在限流外层才能捕获它的 panic（顺序规则见下文）。`ext/warp/tool/` 有参考实现，写自己的也只要几十行。

自定义一个互斥装饰器（同一时刻只有一个工具在跑）：

```go
type lockingTool struct{ types.Tool; mu *sync.Mutex }

func (l lockingTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
    l.mu.Lock()
    defer l.mu.Unlock()
    return l.Tool.Invoke(ctx, args)
}

core.WithToolWarp(func(_ event.Emitter, t types.Tool) types.Tool {
    return lockingTool{t, &sync.Mutex{}}
})
```

挂载在 `ToolRegistry` 上：静态注册（`WithTools`）与钩子运行时注册（`state.Tools.Register`）的工具**都会被包装**，不存在漏网的执行路径。

## 顺序规则（洋葱模型）

多个装饰器按注册序层层包裹，**先注册的在外层**：请求外 → 内，结果内 → 外。

```
toolStart hook（永远在一切 warp 之外）
  → ToolWarp 外层（先注册）
    → ToolWarp 内层
      → tool.Invoke
    ← 内层收尾
  ← 外层收尾
← toolEnd hook（永远在一切 warp 之外）
```

- 需要捕获内层异常的（safetool、modelretry）注册在最外。
- hook 永远在 warp 之外：toolStart 先于一切装饰器，toolEnd 晚于一切装饰器；模型侧同理。

## per-Run 组装

warp 链在**每次 Run 时重新组装**，实例 per-Run 独立：装饰器里持有的状态（计数器、信号量）不跨 Run 共享。跨 Run 需要的状态放在装饰器工厂的闭包里（如上面 `lockingTool` 的 `&sync.Mutex{}` 就活在工厂闭包中）。

## 工具集的确定序

`ToolRegistry.List` 按工具名排序返回——与注册顺序、组装路径、resume、重新 New 全无关。凡进请求序列化的集合都用这种「集合的确定函数」定序：任何路径取同一集合必得同一序列，前缀缓存（KV cache）不会因顺序抖动而全 miss。`types.NewTool` 的 schema 也一样：构造期生成一次，字段声明序即输出序。

## 一个调用一个单元

同一轮的多个工具调用默认全并发（调用数即并发数；要限流用 `limit.Warp(n)` 或自己写信号量装饰器）。每个调用独立跑完整链路：判定 → warp 壳内执行 → 后处理；事件随调用即时发出，消息历史在全部完成后按**原始调用顺序**汇总写入。`LoopParams.SerialTools` 可以强制串行。

---

上一篇：[Loop 引擎](Loop引擎.md)　·　下一篇：[Hook](Hook.md)
