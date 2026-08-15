package fs

import (
	"context"
	"strings"
	"testing"
)

// 核心能力：基础读写 + 路径沙箱 + Edit/ApplyPatch 原子性 + Grep/Find。
func TestLocalCore(t *testing.T) {
	fsys := NewLocal(t.TempDir())
	ctx := context.Background()

	if err := fsys.Write(ctx, "sub/a.txt", []byte("alpha beta\nbeta")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := fsys.Read(ctx, "sub/a.txt")
	if err != nil || string(data) != "alpha beta\nbeta" {
		t.Fatalf("read: %q %v", data, err)
	}
	entries, _ := fsys.List(ctx, "sub")
	if len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("list: %+v", entries)
	}
}

func TestLocalRejectsPathEscape(t *testing.T) {
	fsys := NewLocal(t.TempDir())
	if err := fsys.Write(context.Background(), "../escape.txt", nil); err == nil {
		t.Fatal("path escape must be rejected")
	}
}

func TestEditAndAtomicPatch(t *testing.T) {
	fsys := NewLocal(t.TempDir())
	ctx := context.Background()
	_ = fsys.Write(ctx, "a.txt", []byte("alpha beta"))
	_ = fsys.Write(ctx, "b.txt", []byte("hello"))

	n, err := fsys.Edit(ctx, "a.txt", "beta", "BETA")
	if err != nil || n != 1 {
		t.Fatalf("edit: n=%d err=%v", n, err)
	}
	if _, err := fsys.Edit(ctx, "a.txt", "missing", "x"); err == nil {
		t.Fatal("missing old_text must error")
	}

	if err := fsys.ApplyPatch(ctx, []PatchOp{
		{Path: "a.txt", OldText: "alpha", NewText: "ALPHA"},
		{Path: "b.txt", OldText: "hello", NewText: "HELLO"},
	}); err != nil {
		t.Fatalf("patch: %v", err)
	}
	// 第二个 op 失败 → 原子性：第一个 op 也不生效。
	err = fsys.ApplyPatch(ctx, []PatchOp{
		{Path: "a.txt", OldText: "ALPHA", NewText: "nope"},
		{Path: "b.txt", OldText: "absent", NewText: "x"},
	})
	if err == nil {
		t.Fatal("invalid op must fail")
	}
	a, _ := fsys.Read(ctx, "a.txt")
	if strings.Contains(string(a), "nope") {
		t.Fatal("atomicity violated")
	}
}

func TestGrepAndFind(t *testing.T) {
	fsys := NewLocal(t.TempDir())
	ctx := context.Background()
	_ = fsys.Write(ctx, "app.go", []byte("todo: fix"))
	_ = fsys.Write(ctx, "sub/lib.go", []byte("todo: more"))

	matches, err := fsys.Grep(ctx, GrepRequest{Pattern: "todo", Path: ".", Glob: "*.go"})
	if err != nil || len(matches) != 2 {
		t.Fatalf("grep: %+v err=%v", matches, err)
	}
	if matches[0].Path != "app.go" || matches[0].Line != 1 {
		t.Fatalf("grep match: %+v", matches[0])
	}
	found, err := fsys.Find(ctx, ".", "*.go")
	if err != nil || len(found) != 2 {
		t.Fatalf("find: %v err=%v", found, err)
	}
}
