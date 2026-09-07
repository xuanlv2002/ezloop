package filetools

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

type fakeFS struct{ files map[string][]byte }

func (f fakeFS) Read(_ context.Context, p string) ([]byte, error) {
	if d, ok := f.files[p]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}
func (f fakeFS) Write(_ context.Context, _ string, _ []byte) error { return nil }
func (f fakeFS) List(_ context.Context, _ string) ([]fs.Entry, error) {
	return nil, nil
}
func (f fakeFS) Edit(_ context.Context, _, _, _ string) (int, error) { return 0, nil }

var png1x1 = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}

/* readTool 读图返回标记 → OnLoop 转换为插图消息（位置/格式/幂等） */
func TestOnLoopConvertsMarkToImageMessage(t *testing.T) {
	h := New(fakeFS{files: map[string][]byte{"/tmp/a.png": png1x1}})
	tool := readTool(h)
	out, err := tool.Invoke(context.Background(), mustJSON(`{"path":"/tmp/a.png"}`))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(out, `<image_loaded path="/tmp/a.png"/>`) {
		t.Fatalf("read output = %q, want mark", out)
	}
	state := &types.LoopState{Messages: []types.Message{
		{Role: types.RoleUser, Content: "q"},
		{Role: types.RoleTool, ToolCallID: "c1", Content: out},
	}}
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop: %v", err)
	}
	if len(state.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (插图后)", len(state.Messages))
	}
	img := state.Messages[2]
	if img.Role != types.RoleUser || len(img.Images) != 1 || img.Images[0].MimeType != "image/png" {
		t.Fatalf("image msg = %+v", img)
	}
	if want := base64.StdEncoding.EncodeToString(png1x1); img.Images[0].Data != want {
		t.Fatalf("data mismatch")
	}
	if !strings.Contains(img.Content, "[图片已加载: /tmp/a.png]") {
		t.Fatalf("content = %q", img.Content)
	}
	if strings.Contains(state.Messages[1].Content, "<image_loaded") {
		t.Fatalf("mark should be replaced: %q", state.Messages[1].Content)
	}
	// 幂等：再跑一次不重复插入
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop 2: %v", err)
	}
	if len(state.Messages) != 3 {
		t.Fatalf("idempotent rerun inserted again: %d", len(state.Messages))
	}
}

/* 文件缺失：标记替换为失败说明，不插图 */
func TestOnLoopMissingFile(t *testing.T) {
	h := New(fakeFS{files: map[string][]byte{}})
	state := &types.LoopState{Messages: []types.Message{
		{Role: types.RoleTool, ToolCallID: "c1", Content: imageLoadedMark("/gone.png")},
	}}
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop: %v", err)
	}
	if len(state.Messages) != 1 {
		t.Fatalf("missing file should not insert, got %d", len(state.Messages))
	}
	if !strings.Contains(state.Messages[0].Content, "加载失败") {
		t.Fatalf("content = %q", state.Messages[0].Content)
	}
}

func mustJSON(s string) []byte { return []byte(s) }
