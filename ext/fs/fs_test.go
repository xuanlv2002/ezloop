package fs

import (
	"context"
	"testing"
)

// 核心能力：读写 + 列目录 + 查找替换 + 路径沙箱。
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

	n, err := fsys.Edit(ctx, "sub/a.txt", "beta", "BETA")
	if err != nil || n != 2 {
		t.Fatalf("edit: n=%d err=%v", n, err)
	}
	data, _ = fsys.Read(ctx, "sub/a.txt")
	if string(data) != "alpha BETA\nBETA" {
		t.Fatalf("after edit: %q", data)
	}
}

func TestLocalRejectsPathEscape(t *testing.T) {
	fsys := NewLocal(t.TempDir())
	if err := fsys.Write(context.Background(), "../escape.txt", nil); err == nil {
		t.Fatal("path escape must be rejected")
	}
}

func TestEditNotFound(t *testing.T) {
	fsys := NewLocal(t.TempDir())
	ctx := context.Background()
	_ = fsys.Write(ctx, "a.txt", []byte("hello"))
	if _, err := fsys.Edit(ctx, "a.txt", "missing", "x"); err == nil {
		t.Fatal("edit must fail when old_text not found")
	}
}
