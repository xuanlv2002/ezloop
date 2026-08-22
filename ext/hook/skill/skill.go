/*
Package skill 将技能指令按需注入 system prompt。
Keywords 匹配到用户输入（或未配置 Keywords）的 skill 才会被注入，节省 token。
技能源支持代码内定义（New）与文件系统目录加载（NewFromFS）。

文件布局遵循标准 skill 规范：dir/<skill>/SKILL.md 是唯一必需文件，
frontmatter 提供元数据（name/description，缺省回落目录名与正文首行），
frontmatter 之后的正文是指令。scripts/、references/、assets/ 等子资源
由正文相对路径引用（模型用文件工具按需读取），加载器不感知。
*/
package skill

import (
	"context"
	"fmt"
	"strings"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

/* SkillFile 是技能的指令文件名。 */
const SkillFile = "SKILL.md"

type Skill struct {
	Name        string
	Description string
	// Instructions 是注入给模型的完整指令内容（已剥离 frontmatter）。
	Instructions string
	// Keywords 命中用户输入则注入；为空表示总是注入（仅代码内定义用，
	// 文件加载不提供）。
	Keywords []string
	// Path 是 SKILL.md 的 FS 相对路径（宿主提示"全文见此"用）。
	Path string
}

type Hook struct {
	skills []Skill
}

func New(skills ...Skill) *Hook {
	return &Hook{skills: skills}
}

/*
NewFromFS 从文件系统加载技能：dir 下每个含 SKILL.md 的子目录是一个
技能；无 SKILL.md 的目录跳过。
*/
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
		if !e.IsDir {
			continue
		}
		p := dir + "/" + e.Name + "/" + SkillFile
		data, rerr := fsys.Read(ctx, p)
		if rerr != nil {
			continue // 无 SKILL.md 的目录不是技能
		}
		meta, rest := splitFrontmatter(string(data))
		name := strings.TrimSpace(meta["name"])
		if name == "" {
			name = e.Name
		}
		desc := strings.TrimSpace(meta["description"])
		if desc == "" {
			desc = firstLine(rest) // 容错：frontmatter 漏写时回落正文首行
		}
		skills = append(skills, Skill{
			Name:         name,
			Description:  desc,
			Instructions: rest,
			Path:         p,
		})
	}
	return skills, nil
}

/*
splitFrontmatter 剥离 YAML frontmatter（首行 --- 到闭合 ---），只取顶层
扁平字段（name/description/license 等；嵌套块如 metadata: 的缩进子行跳过）。
不引 YAML 依赖——规范必填字段都是扁平标量。无 frontmatter 时原样返回。
*/
func splitFrontmatter(body string) (map[string]string, string) {
	lines := strings.Split(body, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, body
	}
	meta := map[string]string{}
	for i := 1; i < len(lines); i++ {
		raw := lines[i]
		line := strings.TrimSpace(raw)
		if line == "---" {
			rest := strings.Join(lines[i+1:], "\n")
			rest = strings.TrimPrefix(rest, "\n") // 去 frontmatter 后的首个空行
			return meta, rest
		}
		// 跳过缩进行（嵌套块的子项，如 metadata.author）
		if raw != strings.TrimLeft(raw, " \t") {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			k = strings.TrimSpace(k)
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if k != "" && v != "" {
				meta[k] = v
			}
		}
	}
	return nil, body // frontmatter 未闭合，视为普通正文
}

/* firstLine 取正文首个非空行（# 标题去前缀），截 60 rune。 */
func firstLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "#")), "#"))
			r := []rune(line)
			if len(r) > 60 {
				return string(r[:60]) + "…"
			}
			return line
		}
	}
	return ""
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
