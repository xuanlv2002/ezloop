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

## 二、待办（按优先级，2026-08-18 评审后更新）

### 已完成

- [x] **工具 fan-out 并发闸**（原 P0-2，2026-08-18 以 `ext/warp/tool/limit` 落地）
  - 评审更正：原判断「不能靠换 warp 补」不成立——`WithToolWarp` 的 factory 闭包
    在每次 Run 内跨全部工具共享，信号量 warp 恰好就是全局闸。`limit.Warp(n)`
    挂一次覆盖静态注册与 hook 运行时注入的全部工具，引擎保持零改动。
  - 残留（不修）：warp 挡不住 hook 侧的并发弹窗（50 个审批同时到达）——那是
    渲染层聚合问题，不是资源问题。
- [x] **`Usage` 补 cache token**（原 P1-2，2026-08-18）
  - `types.Usage.CachedTokens`；openai Provider 同时解析 OpenAI
    `prompt_tokens_details.cached_tokens` 与 DeepSeek `prompt_cache_hit_tokens`。
  - 成本字段不加：价格是外部知识，由使用方按价目表算。
- [x] **模型错误分类**（原 P1-1，按文档路径解决）
  - 设计本身是对的（重试留在 warp 层，`HTTPError.Retryable()` 是解耦契约），
    引擎内置重试反而破坏「扩展不入核」。已在 README modelretry 条目写明
    「裸用引擎无内置重试，生产建议挂载」。
- [x] **fork 工具继承的不对称性**（原 P2-2，按文档路径解决）
  - 机制修复不成比例（Fork 保留 toolWarps 会双重包装；task hook 拿不到主 warp 链）。
    已在 `task.WithTools` 注释写明：注入工具不带主循环 warp，需要防护自行包装。
- [x] **reasoning 支持**（2026-08-18 顺手补，非本表原条目）
  - 推理模型思考过程此前被 Provider 整个丢弃。现链路：`reasoning_content` →
    `ModelResponse.Reasoning` / 流式 `ReasoningDelta`（`EventReasoningChunk`）→
    入史 `Message.Reasoning`（持久化回放可见）；请求侧不回传（协议要求）。

### P0 — 会随规模真实翻车（当前挂起，附触发条件）

- [ ] **收紧 `LoopState` 的共享可变性**（挂起）
  - 现状：hook 拿 `*LoopState` 读写任意字段，`Metadata` 并发安全全靠注释约定
    （`types/state.go`）。**评审结论：现有 hook 无一在并发回调里写 Metadata**
    （offload 改 result，localsession/summary 在串行 OnEnd），是「未来的雷」
    而非「现在的雷」；只读视角方案会把 7 个小接口翻倍，违背「接口够简单」。
  - 触发条件：第一个需要在 OnToolStart/OnToolEnd 里共享数据的 hook 出现时，
    再做带锁 `MetaSet/MetaGet`（否决只读视角，否决全员付税的锁分片）。

### P2 — 会限制长期演进（挂起，等多模态真需求）

- [ ] **工具接口去字符串化**（挂起）
  - `Invoke` 结果退化成 string、`ToolResult.Err`(error) 与 `Message.Err`(string)
    二元表示，都真；但只有做多模态 / 结构化输出时才值得动——波及全部扩展，
    YAGNI。届时 Err 二元性可顺手统一。

---

## 三、明确「不做」的事（守住哲学）

- 不把重试 / MCP / 审批 / 会话持久化焊进引擎——它们已经正确地待在 warp / hook 层。
- 不为图省事把并发上限做成「全局一个数」糊在引擎里，除非与 P0 的限流闸统一设计。
- 不加第八个 hook / 更多 warp 之前，先收敛现有 7 个 hook 的语义与并发约束。

---

## 四、下一步最值钱的投入

2026-08-18 评审后修正：原 P0 两条中，fan-out 并发闸已以 `ext/warp/tool/limit`
落地（不需要引擎改动）；Metadata 收敛挂起等触发条件。当前结论：

1. **不是再加 hook / warp**——现有 7 个 hook 的语义与并发约束优先收敛；
2. P0-1（Metadata）等第一个真实并发写需求的 hook 出现再动，届时做带锁
   `MetaSet/MetaGet`，不做只读视角；
3. 多模态需求出现时再启动工具接口去字符串化（P2）。

正确性从注释升级成约束的方向不变，但时机跟着真实需求走，不预支复杂度。
