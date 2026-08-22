# ezloop 开发规范

架构与文件说明见 [architecture.md](architecture.md)，事件与上下文管理见 [lifecycle.md](lifecycle.md)。

## 注释规范

**`/* */` 块注释 = 文档（函数助手）**，用于包注释、导出类型、导出函数（及重要非导出函数）的 doc：

- 第一行：一句话职责
- 其后：只写 Why 级约束（不变量、踩过的坑、必须如此的理由），不超过几行
- 教学式长解释（执行顺序、设计决策全过程）写进 docs/dev/，代码里不重复

**`//` 行注释 = 内部算法短注**，用于函数体内：

- 只标非显然的步骤（防御性判断的原因、并发时序、协议细节）
- 禁止 WHAT 型注释（复述代码）、过时注释、与代码重复的解释

```go
/* Lookup 按名解析工具，找不到返回 error（模型幻觉调用会走到这里，
   错误作为工具结果回传模型自纠，不终止 loop）。 */
func (r *ToolRegistry) Lookup(name string) (Tool, error) {
	t, ok := r.tools[name] // map 直查；同名注册已被覆盖语义合并
	...
}
```

注意 `/* */` 不可嵌套，doc 内不要出现 `/*`。

## 错误处理

- **工具错误回传模型自纠，不终止 loop**：Invoke 返回 error → 引擎把 error 作为该调用的 tool 结果写入历史，模型据此重试或调整。这是全框架最重要的错误语义，fork 出错同理（`task failed: ...` 作为结果回传）
- **hook 错误分类**：OnStart/toolStart 等返回 error → 引擎 fail 路径终止 loop（StopReason 如实记录）；EndHook 用 `context.WithoutCancel` 跑，取消场景下仍执行收尾
- **取消统一走 `fail(ctx, ...)` → StopCancelled**，不单独造取消分支
- 持久化类错误不阻断主流程：写入失败记 `state.Metadata["xxx_error"]`，OnEnd 返回 nil
- 短路语义集中在 `hook.Action{Kind, Result}`：短路决策与短路结果是一体，不拆两处

## 并发契约

- **OnEvent 必须并发安全且快速返回**：工具并发执行时（含 tool warp 发的事件）回调可能被多个 goroutine 同时调用；慢回调会拖住并发工具。需要免锁消费用 `RunAsync` 的 Events 通道（channel 天然并发安全）
- **Metadata**：除 OnToolStart / OnToolEnd（跨调用并发，写 Metadata 须自行加锁，参见 task hook 的 accumulateUsage）外，引擎串行调用 hook 回调，读写安全
- **hook 自建 goroutine** 不要读写 LoopState / Metadata——引擎对其并发访问不做保护；跨 goroutine 只走 channel
- **emit / OnEvent 允许并发**（结构性事实：工具是独立并发单元），引擎自身事件仍串行保序
- **Agent 构建后只读**：因此并发 Run 共享同一 Agent 安全；warp 实例 per-Run 组装，状态不跨 Run 共享

## 扩展开发模式

**新 hook**：实现 hook 包中关心的小接口（一个 struct 可实现多个）→ `core.WithHooks(h)` 或由已有 hook 在 OnStart 自动注册工具。模式约定：

- 需要配置的用 Options 模式：`New(opts ...Option)` + `WithXxx` 函数项
- 提供工具的 hook 在 OnStart 自动注册（`state.Tools.Register(Tool())`），使用方 `WithHooks(New())` 一步到位
- 含工具的 hook 包拆两个文件：`hook.go`（生命周期与拦截逻辑）+ `tools.go`（工具定义，全用 `types.NewTool`）
- 人机中断统一 channel 决策模式：`New() (hook, chan<- Decision)` + `state.EmitEvent` 发请求事件，渲染层 OnEvent 里呈现、独立 goroutine 回传（同步回传会死锁）；并发多等待者共享 channel 必须用 ext/hook/internal/await.Router（按 CallID 路由 + 错配暂存 + close 广播重查），"不匹配就丢弃"会造成互饿死锁
- 自定义事件类型加命名空间前缀（如 `approve.denied`、`task.start`），避免与引擎事件冲突

**新 warp**：`warp.Handler[T]` 签名 `func(em event.Emitter, node T) T`，闭包持有自身状态（信号量等）在工厂里建。先注册的在外层；需要捕获内层 panic 的（safetool）注册在最外

**新工具**：用 `types.NewTool(name, desc, fn)` 构造，schema 从参数结构体的 tag 反射生成——`json:"name,omitempty"` 定字段名（omitempty → 非 required，默认全 required）、`desc:"…"` → description、`enum:"a|b|c"` → string enum；支持 string/bool/number/slice/map[string]T/json.RawMessage/嵌套 struct。schema 构造期生成一次，字段声明序即输出序（确定函数，缓存友好）。required 只约束字段出现与否，值级业务校验（空串拒绝等）留在 fn 内（edit_file 的 new_text 允许空串删除语义）。手写实现 `types.Tool` 仅留给需要动态 schema 的场景（如 provider 适配）

**新 provider**：实现 `provider.ModelProvider`（Invoke）；流式能力可选实现 `StreamProvider`。错误带可重试判断时实现 `Retryable() bool`（与 modelretry 的解耦契约，参见 openai.HTTPError）

## 缓存纪律（KV cache）

- **凡进请求序列化的集合，用"集合的确定函数"定序，不用"历史的函数"**：ToolRegistry.List 按工具名排序——与注册顺序/组装路径/resume/重新 New 全无关，任何路径同集合必同序。曾因裸 map 遍历随机序导致前缀缓存全 miss
- system 消息全程唯一（WithSystemPrompt 一条，skill 拼接其上，WithHistory 过滤历史里的 system），多轮不滚雪球
- 给分身/子循环注入说明文字：拼在 input（请求最末尾），不动 seed 前缀；改 system 会从第一条分叉毁缓存复用
- reasoning_content 只在响应侧解析（Message.Reasoning 供持久化/回放），请求侧不回传（协议要求）

## 测试要求

- `go build ./... && go vet ./... && go test ./... -race` 全绿是合入门槛
- 模型侧行为用 `internal/testutil.Scripted(...)` 脚本驱动（按调用顺序吐 ToolCalls/Text），不 mock 接口以外的任何东西
- 并发行为必须有 -race 钉子：引擎的 `TestAgentConcurrentRuns`、task 的并发 fork、limit 的峰值断言
- 行为约束（排序、剥离、隔离）落成显式断言测试，防回归
