/*
Package skilltool 是 skill 的第三层加载：模型用 load_skill 工具按名称
取 SKILL.md 全文 + 技能目录结构。三层分工：
  1. 名称与描述进 systemPrompt（宿主组装，如清单注入）——模型知道有什么可用；
  2. 本工具返回完整指令与文件路径——模型知道怎么用；
  3. 模型按返回的路径用 shell / read_file 调用 scripts 等子资源。

拦截式实现（Invoke 不可达）：目录结构在拦截时实时列出，技能文件
变更后无需重建（列表快照语义与 systemPrompt 固定不冲突——全文加载
本来就是运行时动作）。
*/
package skilltool

import (
	"context"
	"encoding/json"
	"errors"
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

/* Hook 实现 load_skill 拦截。 */
type Hook struct {
	fsys     fs.FileSystem
	dir      string
	disabled func() []string // 返回禁用技能目录名（实时读取设置快照）
}

/* New 创建技能加载工具 hook。disabled 可空：无启停机制时不过滤。 */
func New(fsys fs.FileSystem, dir string, disabled func() []string) *Hook {
	return &Hook{fsys: fsys, dir: dir, disabled: disabled}
}

func (h *Hook) Name() string { return "skilltool" }

/* OnStart 注册 load_skill 工具并注入使用说明（sys 首位重写 base 后追加，每轮重拼幂等）。 */
func (h *Hook) OnStart(_ context.Context, state *types.LoopState) error {
	state.Tools.Register(skillToolShell{})
	if len(state.Messages) > 0 && state.Messages[0].Role == types.RoleSystem {
		state.Messages[0].Content += "\n\n<tool-guide>\nload_skill：接任务先扫技能清单，命中就加载后按其指引执行，" +
			"不要绕过现成技能自己造流程。\n</tool-guide>"
	}
	return nil
}

/* OnToolStart 拦截 load_skill：返回 SKILL.md 全文 + 路径 + 目录结构。 */
func (h *Hook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (ezhook.Action, error) {
	if call.Name != ToolName {
		return ezhook.Proceed, nil
	}
	var args struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(call.Args, &args)

	skills, _ := skill.LoadDir(ctx, h.fsys, h.dir)
	var off []string
	if h.disabled != nil {
		off = h.disabled()
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
		if s.Name == args.Name {
			return ezhook.Skip(h.render(ctx, s)), nil
		}
	}
	names := make([]string, 0, len(skills))
	for _, s := range skills {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return ezhook.Skip(fmt.Sprintf("skill %q 不存在。可用技能：%s", args.Name, strings.Join(names, ", "))), nil
}

/* render 组装返回体：元信息 + 目录结构 + SKILL.md 全文。 */
func (h *Hook) render(ctx context.Context, s skill.Skill) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# skill: %s\n路径: %s\n", s.Name, s.Path)
	if tree := h.dirTree(ctx, s.Path); tree != "" {
		b.WriteString("目录内容:\n" + tree + "\n")
	}
	b.WriteString("脚本可用 bash 执行、资料可用 read_file 读取（路径相对工作目录）。\n--- SKILL.md ---\n")
	b.WriteString(s.Instructions)
	return b.String()
}

/* dirTree 列技能目录内容（目录式：SKILL.md 同级与子目录，深度 2；平铺为空）。 */
func (h *Hook) dirTree(ctx context.Context, skillPath string) string {
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
		entries, err := h.fsys.List(ctx, prefix)
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

/* skillToolShell 是 load_skill 壳工具（拦截式，Invoke 不可达）。 */
type skillToolShell struct{}

func (skillToolShell) Name() string { return ToolName }
func (skillToolShell) Description() string {
	return "按名称加载技能的完整指令（SKILL.md 全文）与技能目录结构" +
		"（含 scripts 等子资源路径）。需要执行某项技能前先调用它获取详细步骤。"
}
func (skillToolShell) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"技能名（系统提示的可用技能列表里的名字）"}},"required":["name"]}`)
}
func (skillToolShell) Invoke(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("load_skill: hook not registered")
}
