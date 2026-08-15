package skill

import (
	"context"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

func TestNewFromFS(t *testing.T) {
	root := t.TempDir()
	fsys := fs.NewLocal(root)
	ctx := context.Background()

	_ = fsys.Write(ctx, "skills/sql.md", []byte("always use parameterized sql"))
	_ = fsys.Write(ctx, "skills/sql.keywords", []byte("db, 数据库"))
	_ = fsys.Write(ctx, "skills/deploy.md", []byte("confirm before deploy"))
	_ = fsys.Write(ctx, "skills/notes.txt", []byte("should be ignored"))

	h, err := NewFromFS(ctx, fsys, "skills")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(h.skills) != 2 {
		t.Fatalf("want 2 skills (.md only), got %d", len(h.skills))
	}

	// 关键词命中 sql；deploy 无 keywords 文件 → 总是注入。
	state := &types.LoopState{
		Input:    "查一下数据库",
		Messages: []types.Message{{Role: types.RoleUser, Content: "查一下数据库"}},
	}
	if err := h.OnStart(ctx, state); err != nil {
		t.Fatalf("err: %v", err)
	}
	sys := state.Messages[0].Content
	if !strings.Contains(sys, "parameterized sql") || !strings.Contains(sys, "deploy") {
		t.Fatalf("injected content: %q", sys)
	}

	// sql 未命中时不注入。
	state2 := &types.LoopState{
		Input:    "闲聊",
		Messages: []types.Message{{Role: types.RoleUser, Content: "闲聊"}},
	}
	_ = h.OnStart(ctx, state2)
	if !strings.Contains(state2.Messages[0].Content, "deploy") ||
		strings.Contains(state2.Messages[0].Content, "parameterized") {
		t.Fatalf("unmatched sql should be skipped: %q", state2.Messages[0].Content)
	}
}

func TestNewFromFSEmptyDir(t *testing.T) {
	fsys := fs.NewLocal(t.TempDir())
	h, err := NewFromFS(context.Background(), fsys, "skills")
	if err != nil {
		t.Fatalf("empty dir should not error: %v", err)
	}
	if len(h.skills) != 0 {
		t.Fatalf("want 0 skills, got %d", len(h.skills))
	}
}
