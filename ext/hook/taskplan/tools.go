/*
tools.go 定义 task_plan 壳工具：schema 是唯一实体（planArgs tag 反射生成），
真正的"执行体"是 Hook.OnToolStart 拦截后等用户处置，Invoke 不会被走到。
*/
package taskplan

import (
	"context"
	"errors"

	"github.com/xuanlv2002/ezloop/types"
)

type planArgs struct {
	Plan string `json:"plan" desc:"完整的任务规划，分步骤列出"`
}

/*
Tool 返回 task_plan 壳工具：仅提供 schema 供模型发现，
真正的"执行体"是 Hook 拦截后等用户处置，Invoke 不会被走到。
hook 已在 OnStart 自动注册本工具，通常无需手动调用。
*/
func Tool() types.Tool {
	return types.NewTool(ToolName,
		"提交任务规划等待用户处置：用户将选择执行、拒绝或给出修改意见。开始多步任务前先提交规划。",
		func(_ context.Context, _ *planArgs) (string, error) {
			return "", errors.New("taskplan: hook not registered (task_plan is intercepted by taskplan.Hook)")
		})
}
