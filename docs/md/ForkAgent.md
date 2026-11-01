# ForkAgent

`core.Agent.Fork` 是引擎的派生原语：以当前 Agent 为模板，跑一次隔离子循环。典型场景是并行分身（官方的 task 钩子），也可以用于评审、假设分支、重跑验证——任何「以当前自我为模板跑一条隔离线」的需求。

```go
// 原语：seed 是截至当前的消息快照，tools 是本轮可用工具集
sub, err := agent.Fork(ctx, "review-1", seed, tools, "评审这段代码")

// 换模型的分身：视觉子任务交给多模态模型（同样套本 Agent 的 warp 链）
sub, err := agent.ForkWithModel(ctx, forkID, visionModel, seed, tools, input)
```

## 复刻了什么，跳过了什么

Fork 通过浅拷贝 Agent 实现（构建后只读是框架契约，拷贝安全）。与主循环 Run 的差异只有组装期：

| 继承（分身 = 本体） | 跳过（重跑必重复注入） |
| --- | --- |
| provider / model warp 链 / 流式 / 超参 | startHooks（组装期钩子） |
| 全部运行期钩子：审批照拦、可问用户、session 照存 | guideHooks（Guide 附录已在 seed 的 system 里） |
| 工具 warp 壳（传入的 tools 已含主循环包装） | systemPrompt / toolWarps（重新注入或包装会重复） |

seed 是深拷贝——并行 fork 不改写父消息的底层数组。跳过组装期需要触碰 Agent 私有字段，所以 Fork 只能在 core 实现，扩展层做不到；钩子需要派生时经 `core.AgentFromContext(ctx)` 取回引擎再调。

## 过程不回流

分身的中间过程（工具调用、思考）**不进入主循环上下文**。以 task 为例：只有分身最后一条 assistant 消息经 `hook.Skip` 作为工具结果回传主循环——主上下文只留结论，不留过程。这也是控制主循环上下文膨胀的手段：批量生成、多方案试探、超长检索都适合扔给分身。

## forkID 与 SeedLen

引擎结构性填写两个字段，钩子与持久化层据此处理分身：

- `state.ForkID`：分身内所有事件（引擎事件 + 钩子经 `state.EmitEvent` 发的）自动带上它。消费方按 ForkID 分流——空串是主循环，非空是对应分身；多租户里据此把分身的流式输出、审批请求路由到正确的用户。
- `state.SeedLen`：seed 的长度。持久化时据此剥离：分身的存档只写 seed 之后的增量（seed 与主会话重复）。

已知限制：继承工具的 warp 壳持有主循环的 emitter，壳内部事件（重试、降级）不带 ForkID；引擎级事件经子 state 发出，全部带标。

## 单层保证

分身不可再分身：task 工具不在子循环注册，task 钩子对 ForkID 非空的子循环拒绝再次 fork。避免派生树失控。

## 自己封装一个派生入口

绝大多数场景不需要裸调 Fork——在自己的项目里把它封装成模型可调用的工具更顺手（参考实现 `ext/hook/task` 就是一个 40 行的 ToolStart 钩子：拦截 `task` 调用 → `AgentFromContext` 取回引擎 → `ForkWithModel` 跑分身 → `hook.Skip(最终答复)` 回传）。自己封装时的三个要点：

- **任务描述必须自包含**：分身看不到主循环的对话语境，描述里写清目标、输入、涉及的文件绝对路径与期望返回格式——这个要求写进工具的 description，让模型照办。
- **同批无依赖的子任务并行发多个**：每个分身是独立的隔离循环，天然并发。
- **只回传结论**：Skip 的 result 只放最终答复，中间过程留在分身自己的历史里。

---

上一篇：[Hook](Hook.md)　·　下一篇：[最佳实践](最佳实践.md)
