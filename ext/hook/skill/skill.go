// Package skill 将预定义技能指令按需注入 system prompt。
// Keywords 匹配到用户输入（或未配置 Keywords）的 skill 才会被注入，节省 token。
// 技能源支持代码内定义（New）与文件系统目录加载（NewFromFS）。
package skill

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/xuanlv2002/ezloop/ext/fs"
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

// NewFromFS 从文件系统加载技能：dir 下每个 *.md 文件是一个技能，
// 文件名（去扩展名）为技能名，文件内容为 Instructions。
// 可选同名 .keywords 文件（逗号分隔）提供关键词。
func NewFromFS(ctx context.Context, fsys fs.FileSystem, dir string) (*Hook, error) {
	skills, err := LoadDir(ctx, fsys, dir)
	if err != nil {
		return nil, err
	}
	return New(skills...), nil
}

func LoadDir(ctx context.Context, fsys fs.FileSystem, dir string) ([]Skill, error) {
	entries, err := fsys.List(ctx, dir)
	if err != nil {
		// 目录不可访问视为无技能（可选目录），不报错。
		return nil, nil
	}
	var skills []Skill
	for _, e := range entries {
		if e.IsDir || !strings.HasSuffix(e.Name, ".md") {
			continue
		}
		data, rerr := fsys.Read(ctx, dir+"/"+e.Name)
		if rerr != nil {
			continue
		}
		s := Skill{
			Name:         strings.TrimSuffix(e.Name, path.Ext(e.Name)),
			Instructions: string(data),
		}
		// 可选 keywords 文件：skill.md 对应 skill.keywords
		kwData, kerr := fsys.Read(ctx, dir+"/"+strings.TrimSuffix(e.Name, ".md")+".keywords")
		if kerr == nil {
			for kw := range strings.SplitSeq(string(kwData), ",") {
				if kw = strings.TrimSpace(kw); kw != "" {
					s.Keywords = append(s.Keywords, kw)
				}
			}
		}
		skills = append(skills, s)
	}
	return skills, nil
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
	// 拼接到已有 system（WithSystemPrompt 注入的那条）而非新增消息：
	// 全程单条 system，协议面干净、system 前缀缓存友好。
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content += "\n\n" + b.String()
		return nil
	}
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
