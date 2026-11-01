# Loop 引擎

`core.Agent.Run` 的执行流程、消息序列的构造规则、错误语义与恢复模型。这一篇是其余各篇的地基。

## 一次 Run 的流程

```
state = 新 LoopState（工具注册表、事件出口）
消息序列构造：system → WithHistory 历史 → 本次 input
    ↓
① startHooks（组装期，一次）：注册工具、修理历史、动态注入
    ↓
② GuideHook 声明统一追加到 system 尾部
    ↓
循环（≤ MaxIterations，默认 16）：
    ③ modelStart hooks（可置 state.Stop 提前终止）
    ④ 模型调用（ModelWarp 链内；流式时 chunk 实时透出）
    ⑤ modelEnd hooks
    ├─ 无工具调用 → 完成（completed）
    ├─ 有工具调用：
    │    ⑥ toolStart 判定（跨调用并发；Skip/Abort 短路）
    │    ⑦ 工具执行（ToolWarp 链内，每个调用独立并发）
    │    ⑧ toolEnd 后处理（可改写结果）
    │    ⑨ 结果按原始顺序写入历史
    │    ⑩ loopHook（回边：压缩、热加载等）
    │    ↺ 回到 ③
⑪ endHooks（defer 语义，无论成败都执行）
```

## 消息序列的构造

一次 Run 里发给模型的消息按这个顺序构造：

1. **system**：`WithSystemPrompt` 的内容（若设置）。
2. **历史**：`WithHistory(messages...)` 传入的消息，其中的 system 消息被过滤——system 是 agent 属性，每轮由引擎重新注入，全程单条。
3. **本次输入**：一条 user 消息。
4. **startHook 组装**：钩子在此注册工具、修理历史、按输入动态注入内容。
5. **Guide 追加**：引擎收集全部 `Guide()` 声明，按注册序追加到 system 尾部。无 system 且有声明时创建 system 消息。

之后循环体内的消息只增不改：assistant 响应、tool 结果按序追加。`state.Messages` 任何时刻都是一份合法的、可直接序列化的对话转录。

## 迭代上限与软收尾

达到 `MaxIterations`（`WithMaxIterations` / `WithLoopParams` 设置）时不是硬截断：引擎注入一条收尾提醒，再发一次**不带工具**的模型调用，让它基于已做的一切给出总结性答复。StopReason 保持 `max_iteration`，UI 可据 `final_wrap` 事件标注「正在总结收尾」。

## 错误语义

这是全框架最重要的一组约定：

| 情形 | 行为 |
| --- | --- |
| 工具 Invoke 返回 error | **不终止循环**。错误作为该调用的工具结果写入历史，模型据此自纠重试 |
| toolStart 钩子返回 Skip | 工具不执行，`Skip(result)` 的 result 作为工具结果入史（审批拒绝走这条） |
| toolStart 钩子返回 Abort | 终止整个循环，StopReason = `aborted` |
| 其它钩子返回 error | 终止循环，StopReason = `error`，错误带钩子名 |
| 模型调用失败 | 走 `fail` 路径终止（重试在 ModelWarp 层做，如 modelretry） |
| ctx 取消 | StopReason = `cancelled`；流式半截内容抢救入史 |
| 任一钩子 panic | 引擎捕获为 error，单个扩展的崩溃不炸循环 |

EndHook 用脱离取消的 context 执行（`context.WithoutCancel`）——收尾清理（关连接、写快照）不应随请求取消而失败。

## 历史协议完整（核心不变量）

无论本轮以何种方式结束（完成 / Skip / Abort / 钩子报错 / 取消），**每个已写入历史的 tool_call 都有对应的工具结果消息**：

- 正常执行：真实结果。
- 审批拒绝：拒绝理由。
- 钩子报错后未完成的调用：占位结果（`not executed: <name>`）。

因此发给模型的消息序列永远满足协议要求，持久化和恢复不需要理解断点。外部带入的历史可能破损（比如手工编辑过）：在 OnStart 里做一次双向修理——补缺失的 tool 结果、删孤儿 tool 消息（参考实现 `ext/hook/contextfix`）。

## 事件一览

| 事件 | 时机 | Data |
| --- | --- | --- |
| `loop_start` / `loop_end` | Run 起 / 止 | input / StopReason |
| `model_start` / `model_end` | 模型调用前后 | nil / `*ModelResponse` |
| `model_chunk` / `reasoning_chunk` | 流式正文 / 思考增量 | string |
| `tool_start` / `tool_end` | 工具调用起 / 止 | `*ToolCall` / `*ToolResult` |
| `iteration_end` | 每轮迭代结束 | int |
| `error` | 引擎错误 | error |
| `stream_fallback` | 声明流式但链上无 StreamProvider | string |

事件只做观察，不用于改状态。`tool_start` / `tool_end` 随调用即时发出（工具是并发单元，到达顺序不保证）——以 CallID 关联，顺序不是可靠性的来源。扩展自定义事件带命名空间前缀（`task.start`、`approve.request`…）。

## LoopState 关键字段

| 字段 | 说明 |
| --- | --- |
| `Messages` | 全部消息历史，可序列化，可直接作为下一轮 `WithHistory` |
| `Tools` | `*ToolRegistry`：工具注册表，钩子可在组装期动态注册 |
| `Usage` | PromptTokens / CompletionTokens / CachedTokens |
| `Iteration` / `MaxIterations` | 当前迭代与上限 |
| `ForkID` / `SeedLen` | fork 分身身份与 seed 边界（见 [ForkAgent](ForkAgent.md)） |
| `Metadata` | 钩子间共享任意数据；并发契约见 [Hook](Hook.md) |
| `Emitter` | 引擎注入的事件出口，钩子发自定义事件用 |

## 多轮与恢复

恢复 = 新对话：把旧消息 `WithHistory` 传回 + 新的 user 输入，模型看历史自己决定是否重发调用。引擎不记录「执行到哪一步」，也不需要——历史本身就是全部状态。

---

上一篇：[快速开始](快速开始.md)　·　下一篇：[Warp 装饰器](Warp装饰器.md)
