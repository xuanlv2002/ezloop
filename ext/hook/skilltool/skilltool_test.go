package skilltool

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/ext/fs"
	ezhook "github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

/* memFS 内存文件系统（测试用）。 */
type memFS map[string][]byte

func (m memFS) Read(_ context.Context, p string) ([]byte, error) {
	if d, ok := m[p]; ok {
		return d, nil
	}
	return nil, os.ErrNotExist
}
func (m memFS) Write(_ context.Context, p string, data []byte) error {
	m[p] = append([]byte(nil), data...)
	return nil
}
func (m memFS) List(_ context.Context, dir string) ([]fs.Entry, error) {
	prefix := strings.TrimSuffix(dir, "/") + "/"
	seen := map[string]fs.Entry{}
	for p := range m {
		if !strings.HasPrefix(p, prefix) {
			continue
		}
		rest := strings.TrimPrefix(p, prefix)
		parts := strings.Split(rest, "/")
		if len(parts) == 1 {
			seen[rest] = fs.Entry{Name: rest}
		} else {
			seen[parts[0]] = fs.Entry{Name: parts[0], IsDir: true}
		}
	}
	out := make([]fs.Entry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	return out, nil
}
func (m memFS) Edit(_ context.Context, p, oldText, newText string) (int, error) {
	d, ok := m[p]
	if !ok {
		return 0, os.ErrNotExist
	}
	n := strings.Count(string(d), oldText)
	if n == 0 {
		return 0, os.ErrNotExist
	}
	m[p] = []byte(strings.ReplaceAll(string(d), oldText, newText))
	return n, nil
}

func newTestState(msgs []types.Message) *types.LoopState {
	return &types.LoopState{Messages: msgs, Tools: types.NewToolRegistry(), Metadata: map[string]any{}}
}

/* 三层加载的第 2 层：load_skill 返回 SKILL.md 全文 + 路径 + 目录结构。 */
func TestSkillToolLoad(t *testing.T) {
	ctx := context.Background()
	fsys := memFS{}
	_ = fsys.Write(ctx, "memory/skills/pdf/SKILL.md",
		[]byte("---\nname: pdf\ndescription: 提取 PDF\n---\n\n# PDF 处理\n步骤：pdfplumber"))
	_ = fsys.Write(ctx, "memory/skills/pdf/scripts/extract.py", []byte("print(1)"))
	_ = fsys.Write(ctx, "memory/skills/pdf/references/api.md", []byte("api 文档"))

	h := New(fsys, "memory/skills", nil)
	state := newTestState(nil)
	if err := h.OnStart(ctx, state); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Tools.Lookup(ToolName); err != nil {
		t.Fatal("load_skill must be registered")
	}

	action, err := h.OnToolStart(ctx, state, &types.ToolCall{
		ID: "c1", Name: ToolName, Args: []byte(`{"name":"pdf"}`),
	})
	if err != nil || action.Kind != ezhook.KindSkip {
		t.Fatalf("expect skip action, err=%v kind=%v", err, action.Kind)
	}
	r := action.Result
	if !strings.Contains(r, "memory/skills/pdf/SKILL.md") ||
		!strings.Contains(r, "步骤：pdfplumber") || // 全文（frontmatter 已剥离）
		!strings.Contains(r, "scripts/extract.py") || !strings.Contains(r, "references/api.md") {
		t.Fatalf("load result missing parts: %q", r)
	}
	if strings.Contains(r, "name: pdf") {
		t.Fatal("frontmatter must be stripped from instructions")
	}

	// 未知名：返回可用列表提示
	action, _ = h.OnToolStart(ctx, state, &types.ToolCall{
		ID: "c2", Name: ToolName, Args: []byte(`{"name":"nope"}`),
	})
	if !strings.Contains(action.Result, "pdf") {
		t.Fatalf("unknown skill should list available: %q", action.Result)
	}
}

/* disabled 回调按目录名过滤：禁用技能不可加载也不出现在可用列表。 */
func TestSkillToolDisabled(t *testing.T) {
	ctx := context.Background()
	fsys := memFS{}
	_ = fsys.Write(ctx, "skills/pdf/SKILL.md", []byte("---\nname: pdf---\n\nPDF 步骤"))
	_ = fsys.Write(ctx, "skills/csv/SKILL.md", []byte("---\nname: csv---\n\nCSV 步骤"))

	h := New(fsys, "skills", func() []string { return []string{"pdf"} })
	state := newTestState(nil)
	_ = h.OnStart(ctx, state)

	action, _ := h.OnToolStart(ctx, state, &types.ToolCall{
		ID: "c1", Name: ToolName, Args: []byte(`{"name":"pdf"}`),
	})
	if strings.Contains(action.Result, "PDF 步骤") {
		t.Fatalf("disabled skill must not load: %q", action.Result)
	}
	if !strings.Contains(action.Result, "csv") {
		t.Fatalf("available list should contain csv: %q", action.Result)
	}
}
