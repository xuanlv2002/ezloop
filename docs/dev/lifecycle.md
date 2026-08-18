# 事件与上下文管理

本地 Agent 与多租户（Web/服务端）Agent 的共用底座。架构见 [architecture.md](architecture.md)，规范见 [conventions.md](conventions.md)。

## 事件全景

`event.Event{Type, Timestamp, Iteration, ForkID, Data}`。事件只做观察（含流式 chunk），不用于修改状态——修改状态是 Hook 的职责。

### 引擎事件

| 类型 | 时机 | Data |
|---|---|---|
| `loop_start` | Run 开始 | nil |
| `model_start` | 每次模型调用前 | nil |
| `model_chunk` | 流式正文增量 | string |
| `reasoning_chunk` | 流式思考增量（推理模型） | string |
| `model_end` | 模型调用后 | nil |
| `tool_start` | 工具调用开始（随调用即时，到达序不保证，以 CallID 关联） | `*types.ToolCall` |
| `tool_end` | 工具调用结束 | `*types.ToolResult` |
| `iteration_end` | 每轮迭代结束 | nil |
| `error` | 引擎错误 | error |
| `stream_fallback` | 声明流式但链上无 StreamProvider，已降级（不静默） | string |
| `loop_end` | Run 结束 | nil |

### 扩展事件（节选，均带命名空间前缀）

`task.start` / `task.end`（分身起止，Data 为 `*ToolCall` / `*LoopState`）、`approve.request`（审批请求）、`askuser.request`、`taskplan.request`。

### ForkID 归属

fork 子循环内所有事件（引擎事件 + hook 经 `state.EmitEvent` 发的）由引擎结构性打上 `ForkID`——不是 hook 自己记得填。消费方按 ForkID 分流：空串＝主循环，非空＝对应分身。已知限制：继承工具的 warp 壳持有父 emitter，壳内部事件（重试、降级）不带 ForkID。

## 两种事件消费

```go
// 回调：适合 CLI 直渲。并发契约：必须并发安全且快速返回（见 conventions）
core.WithOnEvent(func(e event.Event) { render(e) })

// 通道：适合服务端。loop 结束自动 close，天然并发安全
h := agent.RunAsync(ctx, "task")
defer h.Cancel()          // per-request 取消传播
for e := range h.Events() { /* 按 ForkID 路由到对应用户的流 */ }
state, err := h.Wait()
```

碎 delta 高频直写终端开销大（Windows console 同步写尤其贵），CLI 渲染要攒缓冲定期整块写出（80ms 量级），示例见 examples/chat 的 streamBuf。

## 上下文管理

### LoopState 关键字段

| 字段 | 说明 |
|---|---|
| `Messages` | 全部消息历史，可序列化、可直接作为下一轮 WithHistory——loop 没有隐藏的内存中间态 |
| `Usage` | PromptTokens / CompletionTokens / CachedTokens（fork 用量经 task hook 累加回父） |
| `ForkID` / `SeedLen` | fork 子循环身份与 seed 边界（持久化剥离用），主循环为空/0 |
| `Metadata` | hook 间共享；并发契约见 conventions |
| `Emitter` | 引擎注入，hook 发自定义事件用 |

### 多轮与恢复

**恢复 = 新对话**，不做断点续传：持久恢复就是 `WithHistory(旧消息...)` + 新 user 消息，模型看历史自己重发调用。

- system 是 agent 属性（WithSystemPrompt + skill 注入），WithHistory 自动过滤历史中的 system，每轮由引擎重新注入——全程单条 system
- 外部带入的历史可能协议破损（悬空 tool_call）：contextfix 在 OnStart 双向修理（补缺失 tool 结果 + 删孤儿 tool 消息）
- 大结果走 offload 卸载：超阈值写入 FS，上下文只留摘要+路径，防单轮工具输出撑爆上下文
- 会话持久化 localsession：滚动快照 `sessions/<id>.json`；分身写 `sessions/<主ID>-<forkID>.json` 只存增量（SeedLen 剥离，seed 与主 session 重复）

### fork 分身的上下文

task hook 拦截 task 调用 → `core.Fork`：

- seed＝截至本轮之前的快照（剔除本轮 assistant tool_call 消息**与触发指令**——任务描述以 task 参数为准，schema 要求自包含）
- input＝`taskInputPrefix + 任务描述`（包装文字拼在请求最末尾，不动 seed 前缀）
- 过程上下文（分身的中间工具调用、思考）不回流主上下文，只有最后一条 assistant 消息经 `hook.Skip` 作为工具结果回传
- 分身继承全部运行期 hook：审批照拦、可问用户（人机事件带 ForkID，渲染层区分是哪个分身在问）

## 多租户（Web/服务端）要点

ezloop 的并发模型天然适配"一个 Agent 定义、N 个并发会话"：

1. **共享构建**：Agent 构建后只读 → 可全局单例；每次 Run 内部状态全在独立 LoopState
2. **per-request 事件流**：RunAsync 的 Events() 通道每请求独立，天然隔离分发；回调式 OnEvent 是全局共享的，多租户不要用回调式（或只做度量）
3. **取消传播**：HTTP 断连 → `h.Cancel()`，只取消该请求的 loop，不影响共享 Agent
4. **会话隔离**：每租户/会话一个 localsession 实例（或 WithDir 按租户分目录）；Agent 共享、session hook 实例化按租户——hook 里只有 localsession 这类有状态的需要 per-tenant，无状态 hook（contextfix 等）共享
5. **人机交互 Web 桥**：approve/askuser 的事件带 CallID → 前端渲染卡片 → 用户操作从任意 goroutine 回传 Decision channel（select ctx.Done 防悬挂）；多个分身的请求靠 ForkID 区分归属路由到对的流
6. **持久化错误**不阻断请求：查 `state.Metadata["localsession_error"]` 上报
