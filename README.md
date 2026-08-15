<div align="center">

# ezloop

**一个 Loop · 两个节点 · 七个钩子**

用节点装饰器和流式插件组装任意智能体

[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](https://go.dev)
[![Go Reference](https://pkg.go.dev/badge/github.com/xuanlv2002/ezloop.svg)](https://pkg.go.dev/github.com/xuanlv2002/ezloop)
[![No Heavy Deps](https://img.shields.io/badge/framework-zero%20deps-green)](#包结构)
[![Tests](https://img.shields.io/badge/tests-passing-brightgreen)](#测试)

*model 和 tool 管节点怎么执行，hook 管流程什么时候插入逻辑。*

</div>

---

## 为什么是 ezloop

大多数 Agent 框架把重试、MCP、审批、日志焊死在引擎里。ezloop 只保留一个
**model ↔ tool 循环引擎**，其余一切都是可插拔的：

```
                    ┌──────────────────────────────────────────┐
                    │                Agent Loop                │
                    │                                          │
   WithSystemPrompt │   ┌─────────┐   tool calls   ┌────────┐  │
   WithHistory ───► │   │  model  │ ─────────────► │  tool  │  │
                    │   └────┬────┘ ◄───────────── └────┬───┘  │
                    │        │        tool results      │      │
                    │        ▼                          │      │
                    │   最终回答 ◄──────────────────────┘      │
                    │      （无 tool call 即结束）              │
                    └──────────────────────────────────────────┘
                     ▲ modelWarp  ▲ toolWarp  ▲ 7 × hooks
                     │  重试/降级  │ 超时/防护  │ 拦截/注入/清理
```

| 能力 | 传统框架 | ezloop |
|---|---|---|
| 模型重试 | 引擎内置开关 | `WithModelWarp(modelretry.Warp())` |
| 工具防护 | 引擎内置开关 | `WithToolWarp(safetool.Warp())` |
| MCP 接入 | 引擎专用代码 | `WithHooks(mcp.NewHook(cfg))` 一个插件 |
| 工具审批 | 引擎内置开关 | `WithHooks(approve.New(...))` 一个插件 |
| Hook 崩溃 | 你自己 recover | 引擎标准防护，自动带上下文 |

**引擎不理解任何具体能力，它只负责流转。**

## 三个核心概念

| 概念 | 关注点 | 扩展方式 |
|---|---|---|
| **model** | 节点本身：模型调用 | 实现 `provider.ModelProvider` + `WithModelWarp` 中间件 |
| **tool** | 节点本身：工具执行 | 实现 `types.Tool` + `WithToolWarp` 中间件 |
| **hook** | 流的前后：生命周期与控制流 | 实现 hook 小接口 + `WithHooks` 插入 |

## 快速上手

```bash
go get github.com/xuanlv2002/ezloop
```

```go
package main

import (
    "github.com/xuanlv2002/ezloop/core"
    "github.com/xuanlv2002/ezloop/event"
    "github.com/xuanlv2002/ezloop/ext/provider/openai"
    "github.com/xuanlv2002/ezloop/ext/provider/modelretry"
    "github.com/xuanlv2002/ezloop/ext/hook/mcp"
    "github.com/xuanlv2002/ezloop/ext/warp/tool/safetool"
)

func main() {
    p := openai.New(openai.Options{
        BaseURL: "https://api.openai.com/v1",  // 兼容 DeepSeek/vLLM/Ollama
        APIKey:  "sk-xxx",
        Model:   "gpt-4o",
    })

    agent := core.NewAgent(p,
        core.WithSystemPrompt("你是一个严谨的助手"),      // agent 级系统提示词
        core.WithModelWarp(modelretry.Warp()),       // model 节点：重试
        core.WithToolWarp(safetool.Warp()),          // tool 节点：panic 防护
        core.WithHooks(mcp.NewHook(mcpCfg)),         // 流：MCP 工具接入
        core.WithHyperParams(core.HyperParams{      // 超参数
            MaxIterations:  16,
            MaxConcurrency: 4,   // 单轮工具并发上限（消息与事件仍保序）
        }),
        core.WithStreaming(true),                    // 流式输出
        core.WithOnEvent(func(e event.Event) { fmt.Println(e) }),
    )

    // 同步：单轮
    state, err := agent.Run(ctx, "帮我读一下 hello.txt")

    // 多轮：上一轮的 state.Messages 直接作为历史
    state, err = agent.Run(ctx, "再总结一下", core.WithHistory(state.Messages...))

    // 异步：事件通道 + 取消（服务端场景，与 WithOnEvent 二选一）
    h := agent.RunAsync(ctx, "长任务")
    defer h.Cancel()
    for e := range h.Events() { render(e) }   // loop 结束自动 close
    state, err = h.Wait()
}
```

## Loop 全景

```
startHook
└─ 循环体（引擎根据"模型输出是否含 tool call"自动路由回边）
   │
   │  modelStartHook ──► [model] ──► modelEndHook
   │       (预算守卫)      ▲  ▲        (响应改写)
   │                      │  │
   │         ┌────────────┘  └── tool calls
   │         ▼
   │  toolStartHook ──► [tool] ──► toolEndHook
   │  (Skip/Abort 短路)              (审计日志)
   │         │
   │         ▼
   │  loopHook（回边前：max-iteration 守卫 / 上下文压缩 / 配置热加载）
   │
   └─ 无 tool call → 退出
endHook（无论成败都执行，连接清理等）
```

- **工具错误不终止 loop**：错误结果回传模型供其自纠；只有 hook 报错或 `ActionAbort` 才终止
- **Hook 标准防护**：panic 恢复为带 hook 名与阶段的 error，单个扩展崩溃不会炸掉 loop
- **Event 与 Hook 分离**：`OnEvent` 回调实时观察（含流式 chunk），只读不写；改状态是 Hook 的职责
- **自定义事件**：任何 hook 都能通过 `state.EmitEvent("ns.type", data)` 向 OnEvent 推送自己的事件（类型建议加命名空间前缀，如 `approve.denied`），时间戳与迭代号自动补全

## 三维扩展体系

### Warp：节点的扩展点（引擎标准能力）

```go
// model 节点：重试、降级、日志、限流、多模型路由
core.WithModelWarp(modelretry.Warp(), myAuditWarp)

// tool 节点：重试、超时、审计、缓存
// 挂载在 ToolRegistry 上——静态注册与 hook 运行时注入的工具都会被包装
core.WithToolWarp(safetool.Warp(), myToolWarp)

// 也可以直接包装
p := provider.Warp(baseProvider, warp1, warp2)   // 先注册的在外层
t := types.ToolWarp(myTool, toolWarp1)
```

### Hook：流的扩展点

小接口隔离，扩展只实现关心的接口：

| 接口 | 时机 | 典型用途 |
|---|---|---|
| `StartHook` | loop 开始 | 注入工具（如 mcp router） |
| `ModelStartHook` | 调用模型前 | 预算守卫 |
| `ModelEndHook` | 模型响应后 | 响应改写 |
| `ToolStartHook` | 工具调用前 | 权限拦截（Skip / Abort 短路） |
| `ToolEndHook` | 工具调用后 | 审计日志 |
| `LoopHook` | 迭代回边前 | 上下文压缩、配置热加载 |
| `EndHook` | loop 结束 | 资源清理 |

```go
// 一个插件只写业务，健壮性由引擎兜底
type BudgetHook struct{ Remaining int }

func (h *BudgetHook) Name() string { return "budget" }
func (h *BudgetHook) OnModelStart(_ context.Context, s *types.LoopState) error {
    if s.Iteration > h.Remaining {
        s.Stop = true // 提前终止
    }
    return nil
}
```

## MCP：mcpRouter 单工具封装

模型只看到一个 schema 恒定的 `mcp_router` 工具：

- **KV cache 友好**——工具定义属于 prompt 缓存前缀，恒定 schema 不击穿缓存
- **动态配置**——server 列表热加载（`Reload`），无需重启 agent
- **模型自发现**——`action: list_tools` 按需拉取工具清单与 schema
- **ACL / OAuth 内聚**——白名单与授权收敛在 router 内部

```go
cfg := mcp.Config{
    Servers: []mcp.ServerConfig{{
        Name:  "fs",
        Factory: mcp.StreamableHTTP("https://mcp.example.com",        // 官方 go-sdk 接入
            map[string]string{"Authorization": "Bearer xxx"}),
    }},
}
agent := core.NewAgent(p, core.WithHooks(mcp.NewHook(cfg)))
```

## 官方扩展

| 扩展 | 类型 | 说明 |
|---|---|---|
| `ext/fs` | 底座 | FileSystem 核心（Read/Write/List）+ 可选能力 Modifier（Edit/ApplyPatch）、Searcher（Grep/Find）；Local 实现全部能力（root 沙箱、补丁预检+回滚） |
| `ext/provider/openai` | model | OpenAI 兼容 Provider（Invoke + SSE 流式） |
| `ext/warp/model/modelretry` | warp | 模型重试：指数退避，流式仅在未发出 chunk 时重试 |
| `ext/warp/tool/safetool` | warp | 工具防护：panic 恢复 + error 附加上下文 |
| `ext/warp/tool/offload` | warp | 大结果卸载：超阈值写入 FS，上下文只留摘要+路径，写失败降级透传 |
| `ext/hook/mcp` | hook | mcpRouter 单工具封装，内置官方 go-sdk |
| `ext/hook/skill` | hook | 技能注入：代码定义或从 FS 目录加载 *.md（可选 .keywords） |
| `ext/hook/summary` | hook | loop 结束自动生成摘要写入 Metadata |
| `ext/hook/approve` | hook | 工具审批：同步阻塞式 Approver + 轮次式审批 Store（见 examples/approval） |
| `ext/hook/filetools` | hook | 文件工具集：read_file（行分页/50KB 上限）、write、list、edit、apply_patch、grep、find、run_command；按 FS 能力注册，修改走 per-path 队列 |

## 包结构

```
框架层（只含接口与引擎，零扩展依赖）
├── types/      统一结构体 LoopState / Message / Tool + ToolWarp
├── event/      事件定义与 OnEvent 回调
├── hook/       7 个 hook 小接口 + Action 短路语义
├── provider/   ModelProvider / StreamProvider 抽象 + Warp
└── core/       NewAgent 组装 + loop 引擎

扩展层（能力实现，官方 SDK 依赖放这里，不用不引入）
├── ext/provider/openai
├── ext/warp/model/modelretry
├── ext/warp/tool/safetool
├── ext/hook/{mcp,skill,summary,approve}
└── examples/echo
```

## 测试

```bash
go test ./...        # 引擎行为 / 短路语义 / 事件顺序 / MCP 全链路 / SSE 聚合
go run ./examples/echo
```

---

<div align="center">

**ezloop** — 极简不是功能少，是每个能力都长在该长的地方。

</div>
