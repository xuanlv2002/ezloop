/*
tools.go 定义 task 壳工具：schema 是唯一实体（taskArgs tag 反射生成），
真正的"执行体"是 Hook.OnToolStart 拦截后跑隔离子循环，Invoke 不会被走到。
*/
package task

import (
	"context"
	"errors"

	"github.com/xuanlv2002/ezloop/types"
)

type taskArgs struct {
	Task string `json:"task" desc:"子任务的完整描述，包含目标与验收标准"`
}

/*
Tool 返回 task 壳工具：仅提供 schema 供主模型发现，
Invoke 不会被走到（防呆：未挂 Hook 时尽早暴露装配错误）。
*/
func Tool() types.Tool {
	return types.NewTool(ToolName,
		"在隔离上下文中求解子任务：复刻当前模型与上下文独立运行，只把最终结果带回主对话。适合需要多步工具调用、过程细节无需回流的子问题。",
		func(_ context.Context, _ *taskArgs) (string, error) {
			return "", errors.New("task: hook not registered (task is intercepted by task.Hook)")
		})
}
