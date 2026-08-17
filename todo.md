# ezloop 引擎评价与待办

> 一份对当前引擎的诚实评估：先确认哪些是必须守住的设计，再把风险与短板
> 落成可执行的待办（按优先级排序）。每条带代码定位（`file:line`），避免空谈。

---

## 一句话结论

这是一个**设计纪律远超同类的极简内核**——核心正确性想法是对的、甚至领先；
主要代价是「极简」把正确性负担推给了文档约定和 hook 作者，而不是类型系统或
引擎。作为 kernel 它很健康，作为「直接上生产的框架」还缺几块。

---

## 一、必须守住的设计（不要为了补短板而破坏）

这些是当前引擎最值钱的部分，任何重构都应以「不倒退」为前提：

1. **warp / hook 的纵向 / 横向切分**（`warp/warp.go:18-43`、`core/agent.go:55-84`）
   - `Handler[T]` + `Chain` 复用 `net/http` middleware 思维，泛型覆盖 model/tool 两类节点；
   - hook 用「小接口 + `WithHooks` 类型断言归位」，扩展只实现它关心的接口。
2. **`Action` 短路语义**（`hook/hook.go:42-63`）
   - `Skip(result)` 把「跳过」和「模型看到什么」合成一个值；approve / task / askuser 共用。
3. **协议完整的历史**（`core/loop.go:180-183, 300-322`）
   - 每个 tool_call 必有对应 tool 结果，未完成补占位；`Messages` 永远是可恢复的合法转录。
4. **逐层 panic 隔离**（`core/loop.go:357-380`、工具 `Invoke` 单独 recover、`Run` 顶层 defer）。
5. **per-Run 组装 + 注入 Emitter**（`core/loop.go:30-43`、`warp/warp.go:5-7`）
   - warp 实例每轮独立、观察走 emitter 不碰 state。
6. **并发契约写进文档**（`hook/hook.go:4-12`）。

---

## 二、待办（按优先级）

### P0 — 会随规模真实翻车，建议优先处理

- [ ] **收紧 `LoopState` 的共享可变性**
  - 现状：每个 hook 拿到 `*LoopState` 读写任意字段，`Metadata map[string]any`
    是无类型杂物箱（`types/state.go:29`），所有权/顺序无编译期保证，并发安全
    全靠注释约定；`OnToolStart/OnToolEnd` 并发 + 写 `Metadata` 是最大雷区。
  - 方向（二选一或组合）：
    1. 给 `OnToolStart/OnToolEnd` 一个只读视角（编译器可见约束），把写路径收窄；
    2. 把 `Metadata` 换成带锁分片，或提供 `Set/Get` 方法统一并发访问。
  - **Why**：hook 一旦变多，这里是 bug 的聚集地，也是当前架构唯一会规模化翻车的隐患。
- [ ] **给工具 fan-out 加并发上限**
  - 现状：`HyperParams` 注释明写「调用数即并发数，不可配」（`core/agent.go:20-21`、
    `core/loop.go:288-298`）。模型一次吐 50 个 tool call 就是 50 个 goroutine 直接打后端。
  - 问题：这个并发发生在**引擎里、跨多个调用**，单个 tool warp 很难优雅限这个流。
  - **Why**：生产里 rate-limit / DB 连接耗尽 / 外呼风暴的直接来源，且不能靠换 warp 补。

### P1 — 核心韧性，裸用会踩

- [ ] **核心 loop 的模型错误处理加区分**
  - 现状：模型错误直接 `fail()` → `StopError`（`core/loop.go:118-120, 384-392`），
    无 transient/permanent 区分、无内置重试，重试全推给 `modelretry` warp。
  - **Why**：符合「扩展不入核」哲学，但裸 `NewAgent(p).Run()` 对一次抖动整体失败。
  - 方向：要么在 `ModelProvider` 上定义错误分类（`IsRetryable` 之类），要么明确文档
    「生产必挂 modelretry」，二选一，不要默默接受现状。
- [ ] **`Usage` 补齐字段**
  - 现状：只有 `PromptTokens`/`CompletionTokens`（`types/message.go:35-38`），
    无 cache token、无成本；fork 用量靠 hook 手动 `accumulateUsage`（`task.go:204-209`）。
  - **Why**：成本核算/配额是生产硬需求；手动累加是易忘约定。

### P2 — 会限制长期演进，现在记录、择机处理

- [ ] **工具接口去字符串化**
  - 现状：`Invoke(ctx, json.RawMessage) (string, error)`（`types/tool.go:10-15`），
    结果/错误/内容全部退化成 string；且 `ToolResult.Err` 是 `error`（不可序列化）
    而 `Message.Err` 是 `string`，同一件事两个表示。
  - **Why**：要做多模态 / 结构化输出 / typed tool result 时这个接口得重设计，
    越晚动，波及的扩展越多。
- [ ] **fork 工具继承的不对称性**
  - 现状：`Fork` 清掉 `toolWarps` 是因为继承工具已包装过（`core/fork.go`），
    但 `task.WithTools` 注入的额外工具没走主循环 warp 链（`task.go:166-174`），
    fork 专属工具会跳过防护/审计。目前未在文档点出。
  - 方向：要么把额外工具也过一遍主循环 warp，要么在 `WithTools` 文档里明确这个限制。

---

## 三、明确「不做」的事（守住哲学）

- 不把重试 / MCP / 审批 / 会话持久化焊进引擎——它们已经正确地待在 warp / hook 层。
- 不为图省事把并发上限做成「全局一个数」糊在引擎里，除非与 P0 的限流闸统一设计。
- 不加第八个 hook / 更多 warp 之前，先收敛现有 7 个 hook 的语义与并发约束。

---

## 四、下一步最值钱的投入

不是再加 hook / warp，而是：

1. **收紧 `LoopState` 共享可变性**（P0-1）——把正确性从注释升级成约束；
2. **引擎层补 tool fan-out 并发闸**（P0-2）。

这两点是当前架构下仅有的、会随着规模真实翻车的隐患，其余都能靠扩展层补。
