package skill

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

// 代码内定义：关键词匹配注入 + 未命中跳过 + 单条 system。
func TestSkillInject(t *testing.T) {
	h := New(
		Skill{Name: "sql", Instructions: "use parameterized sql", Keywords: []string{"db", "数据库"}},
		Skill{Name: "base", Instructions: "be concise"},
	)
	state := &types.LoopState{
		Input:    "查数据库",
		Messages: []types.Message{{Role: types.RoleUser, Content: "查数据库"}},
	}
	if err := h.OnStart(context.Background(), state); err != nil {
		t.Fatalf("err: %v", err)
	}
	sys := state.Messages[0]
	if sys.Role != types.RoleSystem || !strings.Contains(sys.Content, "parameterized sql") ||
		!strings.Contains(sys.Content, "be concise") {
		t.Fatalf("system: %q", sys.Content)
	}

	// 未命中关键词的 skill 不注入。
	state2 := &types.LoopState{
		Input:    "闲聊",
		Messages: []types.Message{{Role: types.RoleUser, Content: "闲聊"}},
	}
	_ = h.OnStart(context.Background(), state2)
	if strings.Contains(state2.Messages[0].Content, "parameterized") {
		t.Fatal("unmatched skill must be skipped")
	}

	// 已有 system（WithSystemPrompt）时拼接而非新增：全程单条 system。
	state3 := &types.LoopState{
		Input:    "查数据库",
		Messages: []types.Message{{Role: types.RoleSystem, Content: "agent rules"}, {Role: types.RoleUser, Content: "查数据库"}},
	}
	_ = h.OnStart(context.Background(), state3)
	if n := countSystem(state3.Messages); n != 1 {
		t.Fatalf("want single system message, got %d", n)
	}
	if !strings.Contains(state3.Messages[0].Content, "agent rules") ||
		!strings.Contains(state3.Messages[0].Content, "parameterized sql") {
		t.Fatalf("merged system: %q", state3.Messages[0].Content)
	}
}

func countSystem(msgs []types.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == types.RoleSystem {
			n++
		}
	}
	return n
}

// 标准 skill 布局：dir/<skill>/SKILL.md + frontmatter；
// 无 SKILL.md 的目录跳过；平铺 .md 不再是技能。
func TestLoadDir(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	ctx := context.Background()
	_ = fsys.Write(ctx, "skills/pdf/SKILL.md", []byte(
		"---\nname: pdf-processing\ndescription: \"从 PDF 中提取文本和表格\"\n"+
			"license: Apache-2.0\nmetadata:\n  author: example-org\n  version: \"1.0\"\n---\n\n"+
			"# PDF 处理\n\n## 提取文本\n- 使用 pdfplumber\n"))
	_ = fsys.Write(ctx, "skills/pdf/scripts/extract.py", []byte("print('ok')"))
	_ = fsys.Write(ctx, "skills/notes/SKILL.md", []byte("---\ndescription: 笔记\n---\n正文"))
	_ = fsys.Write(ctx, "skills/plain/SKILL.md", []byte("# 标题即描述\n正文"))
	_ = fsys.Write(ctx, "skills/empty/readme.txt", []byte("no SKILL.md"))
	_ = fsys.Write(ctx, "skills/flat.md", []byte("平铺文件，不是技能"))

	skills, err := LoadDir(ctx, fsys, "skills")
	if err != nil || len(skills) != 3 {
		t.Fatalf("skills: %+v err=%v", skills, err)
	}
	byName := map[string]Skill{}
	for _, s := range skills {
		byName[s.Name] = s
	}

	pdf := byName["pdf-processing"]
	if pdf.Path != "skills/pdf/SKILL.md" {
		t.Fatalf("path: %q", pdf.Path)
	}
	if pdf.Description != "从 PDF 中提取文本和表格" {
		t.Fatalf("frontmatter description: %q", pdf.Description)
	}
	if strings.Contains(pdf.Instructions, "---") || !strings.Contains(pdf.Instructions, "pdfplumber") {
		t.Fatalf("instructions must strip frontmatter: %q", pdf.Instructions)
	}
	if !strings.HasPrefix(pdf.Instructions, "# PDF 处理") {
		t.Fatalf("instructions should start with body: %q", pdf.Instructions)
	}

	// frontmatter 无 name：回落目录名
	if notes := byName["notes"]; notes.Name != "notes" || notes.Description != "笔记" {
		t.Fatalf("dir-name fallback: %+v", notes)
	}
	// 无 frontmatter：name 回落目录名，description 回落正文首行标题
	if plain := byName["plain"]; plain.Name != "plain" || plain.Description != "标题即描述" {
		t.Fatalf("plain fallback: %+v", plain)
	}
}

// 描述截断：正文首行超 60 rune 截断。
func TestDescriptionTruncate(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	_ = fsys.Write(context.Background(), "skills/long/SKILL.md",
		[]byte(strings.Repeat("长", 80)))
	skills, err := LoadDir(context.Background(), fsys, "skills")
	if err != nil || len(skills) != 1 {
		t.Fatalf("skills: %+v err=%v", skills, err)
	}
	d := skills[0].Description
	if !strings.HasSuffix(d, "…") || len([]rune(d)) != 61 {
		t.Fatalf("long desc should truncate at 60 runes: %q", d)
	}
}

// frontmatter 解析边界：未闭合 / 缺失 / CRLF。
func TestSplitFrontmatter(t *testing.T) {
	if _, rest := splitFrontmatter("no fm\nbody"); rest != "no fm\nbody" {
		t.Fatalf("no frontmatter must pass through: %q", rest)
	}
	if _, rest := splitFrontmatter("---\nname: x\nno close"); rest != "---\nname: x\nno close" {
		t.Fatalf("unclosed frontmatter must pass through: %q", rest)
	}
	meta, rest := splitFrontmatter("---\r\nname: x\r\ndescription: y\r\n---\r\nbody")
	if meta["name"] != "x" || meta["description"] != "y" || rest != "body" {
		t.Fatalf("crlf handling: %+v rest=%q", meta, rest)
	}
}
