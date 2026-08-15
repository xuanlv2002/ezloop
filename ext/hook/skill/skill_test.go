package skill

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

// 关键词匹配注入 + 未命中跳过 + 从 FS 目录加载。
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
}

func TestNewFromFS(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	ctx := context.Background()
	_ = fsys.Write(ctx, "skills/deploy.md", []byte("confirm first"))
	_ = fsys.Write(ctx, "skills/deploy.keywords", []byte("上线, deploy"))
	_ = fsys.Write(ctx, "skills/readme.txt", []byte("ignored"))

	h, err := NewFromFS(ctx, fsys, "skills")
	if err != nil || len(h.skills) != 1 || h.skills[0].Name != "deploy" {
		t.Fatalf("skills: %+v err=%v", h.skills, err)
	}
	if len(h.skills[0].Keywords) != 2 {
		t.Fatalf("keywords: %v", h.skills[0].Keywords)
	}
}
