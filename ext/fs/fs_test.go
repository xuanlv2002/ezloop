package fs

import (
	"context"
	"strings"
	"testing"
)

func TestLocalReadWriteList(t *testing.T) {
	root := t.TempDir()
	fsys := NewLocal(root)
	ctx := context.Background()

	if err := fsys.Write(ctx, "notes/a.txt", []byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := fsys.Read(ctx, "notes/a.txt")
	if err != nil || string(data) != "hello" {
		t.Fatalf("read: %q %v", data, err)
	}
	entries, err := fsys.List(ctx, "notes")
	if err != nil || len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("list: %+v %v", entries, err)
	}
}

func TestLocalRejectsPathEscape(t *testing.T) {
	root := t.TempDir()
	fsys := NewLocal(root)
	for _, p := range []string{"../escape.txt", "a/../../escape.txt"} {
		if err := fsys.Write(context.Background(), p, nil); err == nil || !strings.Contains(err.Error(), "escapes") {
			t.Fatalf("path %q should be rejected, got %v", p, err)
		}
	}
}
