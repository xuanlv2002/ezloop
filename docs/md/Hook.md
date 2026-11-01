# Hook

hook 是流程切面：在循环的节点前后按**时机**插入逻辑。实现 hook 包里你关心的小接口（一个 struct 可以实现多个），`core.WithHooks` 内部按类型断言自动归位——没有 implements 声明，实现即注册。

ezloop 的预期用法是**每个项目封装自己的 hook**：引擎只给时机与契约，行为写在你自己的代码里。这一篇先讲契约，再给三个可以直接抄走的上手范例。

## 全部接口

| 接口 | 时机 | 典型用途 |
| --- | --- | --- |
| `StartHook` | Run 开始（组装期，一次） | 注册工具、修理历史、按输入动态注入 system |
| `GuideHook`（声明） | 组装期由引擎统一收集 | 静态 system 附录：工具使用说明等固定文本 |
| `ModelStartHook` | 每次模型调用前 | 置 `state.Stop = true` 可提前终止 |
| `ModelEndHook` | 每次模型调用后 | 用量统计 |
| `ToolStartHook` | 每个工具调用前（跨调用并发） | 审批拦截：返回 `Skip` / `Abort` 短路 |
| `ToolEndHook` | 调用后、结果入史前 | 改写结果（如把大结果卸载为文件） |
| `LoopHook` | 每轮迭代回边 | 上下文压缩、配置热加载 |
| `EndHook` | Run 结束（defer 语义，成败都跑） | 快照落盘等收尾 |

`GuideHook` 不是第八个时机点——它没有回调时机，是纯声明：`Guide() string` 返回静态文本，引擎在全部 startHook 之后按注册序统一追加到 system 尾部。

## 声明与调整分离

system 上的内容分两类，走不同通道：

- **静态**（固定不变的使用说明）→ `Guide()`。引擎统一追加，顺序与幂等有结构保证：重写 system 的 hook 注册在任意位置都擦不掉别人的声明；同一声明每次 Run 恰好追加一次。
- **动态**（每轮变化：会话身份、状态快照、按输入匹配的内容）→ `StartHook` 里改 `state.Messages`。

## 短路是一个值

`ToolStartHook` 返回 `hook.Action{Kind, Result}`：

```go
hook.Proceed       // 放行
hook.Skip(result)  // 跳过本次调用，result 作为工具结果写入历史
hook.Abort         // 终止整个循环（StopReason = aborted）
```

「跳过」和「跳过时模型看到什么」是同一个决策，不拆两处。Skip 会短路后续 toolStart 钩子——拒绝必须拦得下来，不能被排在后面的钩子「绕过去继续执行」。

## 三个上手范例

**审计日志（ToolStart）**——最简单的起步，8 行：

```go
type auditLog struct{}

func (auditLog) Name() string { return "audit" }

func (auditLog) OnToolStart(_ context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
    log.Printf("tool %s %s", call.Name, call.Args)
    return hook.Proceed, nil
}
```

**白名单审批（ToolStart + Skip）**——写操作必须放行才执行，拒绝理由回传模型自纠：

```go
type writeGuard struct{ allowed map[string]bool }

func (g writeGuard) Name() string { return "write-guard" }

func (g writeGuard) OnToolStart(_ context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
    if !g.allowed[call.Name] {
        return hook.Skip("此工具未开放，请改用 allowed 清单内的工具完成同样的事"), nil
    }
    return hook.Proceed, nil
}
```

要接人工审批，把「拒绝/放行」换成事件 + channel：`state.EmitEvent("approve.request", call)` 发出去，goroutine 里等决策 channel，按决策返回 Proceed / Skip。人机交互事件的 Data 里带上 CallID，渲染层据此回传。

**会话持久化（EndHook + StartHook）**——每轮结束落盘，下轮开始恢复：

```go
type session struct{ path string }

func (s session) Name() string { return "session" }

func (s session) OnEnd(_ context.Context, state *types.LoopState) error {
    data, _ := json.Marshal(state.Messages)
    return os.WriteFile(s.path, data, 0o600) // 失败想不阻断就记 Metadata 后返回 nil
}

// 恢复侧：宿主代码读盘后 core.WithHistory(msgs...) 传入，不需要 hook 参与
```

三个范例覆盖了三类最常见的需求：观察、拦截、收尾。组合、加配置（Options 模式）、发自定义事件，就是完整的自有扩展。

## 并发契约

引擎对回调的调度方式：

- 除 `OnToolStart` / `OnToolEnd` **跨调用并发**（同一轮多个工具同时判定，多个人工审批同时呈现）外，其余回调全部串行。
- 因此大多数回调里读写 `state` / `Metadata` 是安全的；`OnToolStart` / `OnToolEnd` 里写共享数据必须自行加锁。
- 钩子自建的 goroutine 不要读写 `LoopState` / `Metadata`——引擎对其并发访问不做保护；跨 goroutine 只走 channel。
- 钩子 panic 被引擎捕获为 error（带钩子名），单个扩展崩溃不会炸掉循环。

## system 的组装

一次 Run 里 system 消息的完整构成：

```
WithSystemPrompt 基座
  + startHook 动态注入（按输入匹配的内容等）
  + GuideHook 声明统一追加（按注册序）
```

`WithHistory` 过滤历史里的 system，每轮由引擎重新注入——全程单条，多轮不滚雪球。给 fork 分身注入说明文字时拼在 input（请求末尾）而不是改 system：改 system 会从第一条消息分叉，毁掉前缀缓存。

## 自封装时的几条约定

- 需要配置的用 Options 模式：`New(opts ...Option)` + `WithXxx` 函数项，使用方一行装配。
- 提供工具的钩子在 OnStart 注册（`state.Tools.Register(...)`），工具定义用 `types.NewTool` 让 schema 反射生成。
- 自定义事件类型加自己的命名空间前缀（`myapp.approval` 这样），避免与引擎事件冲突。
- 需要引擎能力（如 Fork）时用 `core.AgentFromContext(ctx)` 在运行期自举，不在组装期持有 Agent 引用。

---

上一篇：[Warp 装饰器](Warp装饰器.md)　·　下一篇：[ForkAgent](ForkAgent.md)
