// Package skill 将预定义技能指令按需注入 system prompt。
// Keywords 匹配到用户输入（或未配置 Keywords）的 skill 才会被注入，节省 token。
package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/xuanlv2002/ezloop/types"
)

type Skill struct {
	Name        string
	Description string
	// Instructions 是注入给模型的完整指令内容。
	Instructions string
	// Keywords 命中用户输入则注入；为空表示总是注入。
	Keywords []string
}

type Hook struct {
	skills []Skill
}

func New(skills ...Skill) *Hook {
	return &Hook{skills: skills}
}

func (h *Hook) Name() string { return "skill" }

func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	var matched []Skill
	for _, s := range h.skills {
		if s.match(state.Input) {
			matched = append(matched, s)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	var b strings.Builder
	for i, s := range matched {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "# skill: %s\n%s", s.Name, s.Instructions)
	}
	// system 消息置于最前，供 provider 作为系统指令解析。
	state.Messages = append([]types.Message{{
		Role:    types.RoleSystem,
		Content: b.String(),
	}}, state.Messages...)
	return nil
}

func (s Skill) match(input string) bool {
	if len(s.Keywords) == 0 {
		return true
	}
	lower := strings.ToLower(input)
	for _, kw := range s.Keywords {
		if strings.Contains(lower, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}
