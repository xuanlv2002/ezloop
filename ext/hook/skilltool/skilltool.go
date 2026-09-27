/*
Package skilltool 是 skill 的第三层加载：模型用 load_skill 工具按名称
取 SKILL.md 全文 + 技能目录结构。三层分工：
 1. 名称与描述进 systemPrompt（宿主组装，如清单注入）——模型知道有什么可用；
 2. 本工具返回完整指令与文件路径——模型知道怎么用；
 3. 模型按返回的路径用 shell / read_file 调用 scripts 等子资源。

工具本体闭环（Invoke 内完成，不依赖 OnToolStart 拦截）：目录结构在调用时
实时列出，技能文件变更后无需重建（列表快照语义与 systemPrompt 固定不
冲突——全文加载本来就是运行时动作）。
*/
package skilltool

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	ezhook "github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/ext/hook/skill"
)

/* ToolName 是按名称加载技能全文的工具注册名。 */
const ToolName = "load_skill"

/* Hook 注册 load_skill 工具并注入使用说明。 */
type Hook struct {
	fsys     fs.FileSystem
	dir      string
	disabled func() []string // 返回禁用技能目录名（实时读取设置快照）
	extra    func() []skill.Skill
}

/* Option 配置 Hook 的可选行为。 */
type Option func(*Hook)

/*
WithExtra 注入非文件系统来源的技能（宿主内嵌分发），与目录技能一起参与
名称解析与禁用过滤；它们不落磁盘，render 不提路径、不列目录。
*/
func WithExtra(fn func() []skill.Skill) Option { return func(h *Hook) { h.extra = fn } }

/* New 创建技能加载工具 hook。disabled 可空：无启停机制时不过滤。 */
func New(fsys fs.FileSystem, dir string, disabled func() []string, opts ...Option) *Hook {
	h := &Hook{fsys: fsys, dir: dir, disabled: disabled}
	for _, o := range opts {
		o(h)
	}
	return h
}

func (h *Hook) Name() string { return "skilltool" }

/* OnStart 注册 load_skill 工具并注入使用说明（sys 首位重写 base 后追加，每轮重拼幂等）。 */
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(&skillTool{fsys: h.fsys, dir: h.dir, disabled: h.disabled, extra: h.extra})
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content += "\n\n<tool-guide>\n" +
			"load_skill：接任务的第一个动作是对照 system 里的技能清单——命中就先调用本工具加载（返回完整指令与脚本路径），" +
			"严格按其指引执行，不要绕过现成技能自己造流程；清单没命中再自行设计。" +
			"改过技能文件后要重新加载，拿到的才是最新版。\n</tool-guide>"
	}
	return nil
}

/* skillTool 是 load_skill 工具本体：解析名称 → 返回 SKILL.md 全文 + 路径 + 目录结构。 */
type skillTool struct {
	fsys     fs.FileSystem
	dir      string
	disabled func() []string
	extra    func() []skill.Skill
}

func (t *skillTool) Name() string { return ToolName }
func (t *skillTool) Description() string {
	return "按名称加载技能的完整指令（SKILL.md 全文）与技能目录结构" +
		"（含 scripts 等子资源路径）。需要执行某项技能前先调用它获取详细步骤。"
}
func (t *skillTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"技能名（系统提示的可用技能列表里的名字）"}},"required":["name"]}`)
}

func (t *skillTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(args, &a)

	skills, _ := skill.LoadDir(ctx, t.fsys, t.dir)
	// 内嵌技能排在目录技能之后：同名时先到先得，磁盘技能可覆盖内嵌
	embedded := map[string]bool{}
	if t.extra != nil {
		for _, s := range t.extra() {
			embedded[s.Path] = true
			skills = append(skills, s)
		}
	}
	var off []string
	if t.disabled != nil {
		off = t.disabled()
	}
	if len(off) > 0 {
		kept := skills[:0]
		for _, s := range skills {
			if !slices.Contains(off, skill.DirOf(s.Path)) {
				kept = append(kept, s)
			}
		}
		skills = kept
	}
	for _, s := range skills {
		if s.Name == a.Name {
			return t.render(ctx, s, embedded[s.Path]), nil
		}
	}
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return fmt.Sprintf("skill %q 不存在。可用技能：%s", a.Name, strings.Join(names, ", ")), nil
}

/* render 组装返回体：元信息 + 目录结构 + SKILL.md 全文（内嵌技能无磁盘路径与子资源）。 */
func (t *skillTool) render(ctx context.Context, s skill.Skill, embedded bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# skill: %s\n", s.Name)
	if embedded {
		b.WriteString("来源: 内建技能（随应用分发，不在工作目录，无子资源文件）\n")
	} else {
		fmt.Fprintf(&b, "路径: %s\n", s.Path)
		if tree := t.dirTree(ctx, s.Path); tree != "" {
			b.WriteString("目录内容:\n" + tree + "\n")
		}
		b.WriteString("脚本可用 bash 执行、资料可用 read_file 读取（路径相对工作目录）。\n")
	}
	b.WriteString("--- SKILL.md ---\n")
	b.WriteString(s.Instructions)
	return b.String()
}

/* dirTree 列技能目录内容（目录式：SKILL.md 同级与子目录，深度 2；平铺为空）。 */
func (t *skillTool) dirTree(ctx context.Context, skillPath string) string {
	dir := skillPath
	if i := strings.LastIndex(dir, "/"); i >= 0 {
		dir = dir[:i]
	} else {
		return ""
	}
	var lines []string
	var walk func(prefix string, depth int)
	walk = func(prefix string, depth int) {
		if depth > 2 {
			return
		}
		entries, err := t.fsys.List(ctx, prefix)
		if err != nil {
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
		for _, e := range entries {
			if e.IsDir {
				lines = append(lines, prefix+"/"+e.Name+"/")
				walk(prefix+"/"+e.Name, depth+1)
			} else {
				lines = append(lines, prefix+"/"+e.Name)
			}
		}
	}
	walk(dir, 1)
	return strings.Join(lines, "\n")
}

/* 确保 hook 接口实现（OnStart 之外无拦截逻辑——工具闭环，不走 OnToolStart）。 */
var _ ezhook.Hook = (*Hook)(nil)
