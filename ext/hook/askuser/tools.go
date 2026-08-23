/*
tools.go 定义 ask_user 壳工具：schema 是唯一实体（askArgs tag 反射生成），
真正的"执行体"是 Hook.OnToolStart 拦截后等用户回答，Invoke 不会被走到。
*/
package askuser

import (
	"context"
	"errors"

	"github.com/xuanlv2002/ezloop/types"
)

type askArgs struct {
	Question string `json:"question" desc:"要问用户的问题"`
}

/*
Tool 返回 ask_user 壳工具：仅提供 schema 供模型发现，
真正的"执行体"是 Hook 拦截后等用户回答，Invoke 不会被走到。
hook 已在 OnStart 自动注册本工具，通常无需手动调用。
*/
func Tool() types.Tool {
	return types.NewTool(ToolName,
		"向用户提问并等待回答。缺少必要信息、需要澄清或确认方向时使用，不要替用户假设。",
		func(_ context.Context, _ *askArgs) (string, error) {
			// 防呆：未挂 Hook 时尽早暴露装配错误。
			return "", errors.New("askuser: hook not registered (ask_user is intercepted by askuser.Hook)")
		})
}
