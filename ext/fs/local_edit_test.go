package fs

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func setupEditFS(t *testing.T) (Local, context.Context) {
	t.Helper()
	fsys := NewLocal(t.TempDir())
	ctx := context.Background()
	_ = fsys.Write(ctx, "a.txt", []byte("alpha beta gamma\nbeta again"))
	_ = fsys.Write(ctx, "sub/b.txt", []byte("hello world"))
	return fsys, ctx
}

func TestLocalEdit(t *testing.T) {
	fsys, ctx := setupEditFS(t)

	n, err := fsys.Edit(ctx, "a.txt", "beta", "BETA")
	if err != nil || n != 2 {
		t.Fatalf("edit: n=%d err=%v", n, err)
	}
	data, _ := fsys.Read(ctx, "a.txt")
	if strings.Count(string(data), "BETA") != 2 {
		t.Fatalf("result: %q", data)
	}

	if _, err := fsys.Edit(ctx, "a.txt", "missing", "x"); err == nil {
		t.Fatal("missing old_text must error")
	}
}

func TestLocalApplyPatchAtomic(t *testing.T) {
	fsys, ctx := setupEditFS(t)

	err := fsys.ApplyPatch(ctx, []PatchOp{
		{Path: "a.txt", OldText: "alpha", NewText: "ALPHA"},
		{Path: "sub/b.txt", OldText: "world", NewText: "WORLD"},
	})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	a, _ := fsys.Read(ctx, "a.txt")
	b, _ := fsys.Read(ctx, "sub/b.txt")
	if !strings.Contains(string(a), "ALPHA") || !strings.Contains(string(b), "WORLD") {
		t.Fatalf("patch result: %q %q", a, b)
	}

	// 第二个 op 的 old_text 不存在 → 全部不生效。
	err = fsys.ApplyPatch(ctx, []PatchOp{
		{Path: "a.txt", OldText: "ALPHA", NewText: "should not apply"},
		{Path: "sub/b.txt", OldText: "nope", NewText: "x"},
	})
	if err == nil {
		t.Fatal("invalid op must fail patch")
	}
	a2, _ := fsys.Read(ctx, "a.txt")
	if strings.Contains(string(a2), "should not apply") {
		t.Fatal("atomicity violated: first op must not be applied")
	}
}

func TestLocalGrep(t *testing.T) {
	fsys, ctx := setupEditFS(t)
	_ = fsys.Write(ctx, "sub/c.txt", []byte("beta in sub"))

	matches, err := fsys.Grep(ctx, GrepRequest{Pattern: "beta", Path: "."})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if len(matches) != 3 {
		t.Fatalf("want 3 matches, got %d: %+v", len(matches), matches)
	}

	onlyTxt, _ := fsys.Grep(ctx, GrepRequest{Pattern: "beta", Path: ".", Glob: "*.txt"})
	if len(onlyTxt) != 3 {
		t.Fatalf("glob filter: %d", len(onlyTxt))
	}

	if _, err := fsys.Grep(ctx, GrepRequest{Pattern: "(", Path: "."}); err == nil {
		t.Fatal("bad regex must error")
	}
}

func TestLocalFind(t *testing.T) {
	fsys, ctx := setupEditFS(t)
	_ = fsys.Write(ctx, "sub/c.txt", []byte("x"))

	found, err := fsys.Find(ctx, ".", "*.txt")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("want 3 files, got %v", found)
	}
	one, _ := fsys.Find(ctx, ".", "b.txt")
	if len(one) != 1 || one[0] != "sub/b.txt" {
		t.Fatalf("find b.txt: %v", one)
	}
	_ = fmt.Sprint(found)
}
