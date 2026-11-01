<div align="center">

<img src="docs/logo.svg" width="130" alt="ezloop">

# ezloop

[![Docs](https://img.shields.io/badge/docs-online-38bdf8?logo=readthedocs)](https://xuanlv2002.github.io/ezloop/)
[![Go Version](https://img.shields.io/badge/go-1.25%2B-00ADD8?logo=go)](https://go.dev)
[![Go Reference](https://img.shields.io/badge/go-reference-00ADD8?logo=go)](https://pkg.go.dev/github.com/xuanlv2002/ezloop)
[![License](https://img.shields.io/badge/license-Apache--2.0-green)](LICENSE)
[![Tests](https://img.shields.io/badge/tests-passing-brightgreen)](#测试)

</div>

## 一个只做循环的 Agent 引擎。

你提供模型 provider 和工具，ezloop 驱动「模型 → 工具 → 模型」循环直到任务完成；消息历史全程协议完整，可直接存盘、直接作为下一轮历史。重试、限流、审批、MCP、会话持久化全部是可选扩展——引擎本身零第三方依赖。

## 快速开始

```go
agent := core.NewAgent(p,
    core.WithSystemPrompt("你是一个严谨的助手"),
    core.WithHooks(auditLog{}) // ← 你的第一个 hook，实现见下
)

state, _ := agent.Run(ctx, "帮我读一下 hello.txt")

// 多轮：上一轮 Messages 直接作为历史，没有需要维护的会话对象
state, _ = agent.Run(ctx, "再总结一下", core.WithHistory(state.Messages...))
```

hook 是普通 struct，实现多少接口就有多少能力——这个把每次工具调用记进审计日志的 hook 只要 8 行：

```go
type auditLog struct{}

func (auditLog) Name() string { return "audit" }

func (auditLog) OnToolStart(_ context.Context, _ *types.LoopState, call *types.ToolCall) (hook.Action, error) {
    log.Printf("tool %s %s", call.Name, call.Args)
    return hook.Proceed, nil
}
```

完整上手与七个时机点见[文档 · 快速开始](https://xuanlv2002.github.io/ezloop/)。

## 与「全家桶」框架的差别

| | 全功能框架 | ezloop |
| --- | --- | --- |
| 引擎体积 | 能力内置，依赖随能力增长 | 只做循环 / 历史 / 事件，零依赖 |
| 接入新能力 | 等框架支持，或绕过它 | 写一个 warp 或 hook，几十行的事 |
| 状态 | 会话对象与隐藏内存态 | 就是 `Messages`，可序列化、可恢复 |
| 并发 | 各家自行约定 | 构建后只读：全局单例 + 并发 Run + Fork 浅拷贝 |
| 不用的能力 | 也编译进你的二进制 | 不 import 就不存在 |

## 关键特性

- **两类节点装饰器（warp）**：包在模型调用与工具执行外层的中间件——重试、限流、防护都这么写
- **七个时机钩子 + Guide 声明**：静态说明声明一次由引擎收集；动态行为按注册序插在对应时机
- **历史协议完整**：任何退出路径下每个 tool_call 必有对应结果，恢复即重放历史
- **短路是一个值**：`Skip(result)` / `Abort`——审批拦截这类「拒绝并告知原因」一行搞定
- **ForkAgent 派生原语**：以当前 Agent 为模板跑隔离子循环，过程不回流主上下文
- **事件流**：流式增量、工具起止全程透出，带 ForkID 归属

## 关于官方扩展

`ext/` 附带的少量实现（OpenAI 兼容 provider、重试 / 限流 / 防护 warp 等）**仅作参考**，不承诺长期维护。建议在自己的项目里按需封装 hook 与 warp——怎么写，见文档 [Hook](https://xuanlv2002.github.io/ezloop/) 与 [Warp 装饰器](https://xuanlv2002.github.io/ezloop/)。

## 文档

[设计理念](docs/md/设计理念.md) · [快速开始](docs/md/快速开始.md) · [Loop 引擎](docs/md/Loop引擎.md) · [Warp 装饰器](docs/md/Warp装饰器.md) · [Hook](docs/md/Hook.md) · [ForkAgent](docs/md/ForkAgent.md) · [最佳实践](docs/md/最佳实践.md)

## 测试

```bash
go test ./... -race
```

## 许可协议

[Apache-2.0](LICENSE)
