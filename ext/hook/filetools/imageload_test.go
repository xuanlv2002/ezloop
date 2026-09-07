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
		{Role: types.RoleAssistant, Content: "", ToolCalls: []types.ToolCall{{ID: "c1", Name: "read_file"}}},
		{Role: types.RoleTool, ToolCallID: "c1", Content: out},
	}}
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop: %v", err)
	}
	if len(state.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (插图后)", len(state.Messages))
	}
	img := state.Messages[3]
	if img.Role != types.RoleUser || len(img.Images) != 1 || img.Images[0].MimeType != "image/png" {
		t.Fatalf("image msg = %+v", img)
	}
	if want := base64.StdEncoding.EncodeToString(png1x1); img.Images[0].Data != want {
		t.Fatalf("data mismatch")
	}
	if !strings.Contains(img.Content, "[图片已加载: /tmp/a.png]") {
		t.Fatalf("content = %q", img.Content)
	}
	if strings.Contains(state.Messages[2].Content, "<image_loaded") {
		t.Fatalf("mark should be replaced: %q", state.Messages[2].Content)
	}
	// 幂等：再跑一次不重复插入
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop 2: %v", err)
	}
	if len(state.Messages) != 4 {
		t.Fatalf("idempotent rerun inserted again: %d", len(state.Messages))
	}
}

/* 批内多个标记合并为一条 user 消息（多图） */
func TestOnLoopMergesBatchMarks(t *testing.T) {
	h := New(fakeFS{files: map[string][]byte{"/tmp/a.png": png1x1, "/tmp/b.png": png1x1}})
	state := &types.LoopState{Messages: []types.Message{
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "c1"}, {ID: "c2"}}},
		{Role: types.RoleTool, ToolCallID: "c1", Content: imageLoadedMark("/tmp/a.png")},
		{Role: types.RoleTool, ToolCallID: "c2", Content: imageLoadedMark("/tmp/b.png")},
	}}
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop: %v", err)
	}
	if len(state.Messages) != 4 {
		t.Fatalf("messages = %d, want 4 (一条合并消息)", len(state.Messages))
	}
	img := state.Messages[3]
	if img.Role != types.RoleUser || len(img.Images) != 2 {
		t.Fatalf("merged msg = %+v", img)
	}
	if img.Content != "[图片已加载: /tmp/a.png、/tmp/b.png]" {
		t.Fatalf("content = %q", img.Content)
	}
}

/* assistant 边界：之前的残留标记（取消轮落盘）不补偿 */
func TestOnLoopStopsAtAssistantBoundary(t *testing.T) {
	h := New(fakeFS{files: map[string][]byte{"/tmp/a.png": png1x1}})
	state := &types.LoopState{Messages: []types.Message{
		{Role: types.RoleTool, ToolCallID: "c0", Content: imageLoadedMark("/tmp/a.png")}, // 上批残留
		{Role: types.RoleAssistant, Content: "answer"},
		{Role: types.RoleUser, Content: "q"},
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "c1"}}},
		{Role: types.RoleTool, ToolCallID: "c1", Content: imageLoadedMark("/tmp/a.png")},
	}}
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop: %v", err)
	}
	if len(state.Messages) != 6 {
		t.Fatalf("messages = %d, want 6 (只处理边界后的)", len(state.Messages))
	}
	if !strings.Contains(state.Messages[0].Content, "<image_loaded") {
		t.Fatalf("stale mark should be left untouched: %q", state.Messages[0].Content)
	}
	if !strings.Contains(state.Messages[5].Content, "[图片已加载") {
		t.Fatalf("new mark should convert: %q", state.Messages[5].Content)
	}
}

/* 文件缺失：标记替换为失败说明，不插图 */
func TestOnLoopMissingFile(t *testing.T) {
	h := New(fakeFS{files: map[string][]byte{}})
	state := &types.LoopState{Messages: []types.Message{
		{Role: types.RoleAssistant, ToolCalls: []types.ToolCall{{ID: "c1"}}},
		{Role: types.RoleTool, ToolCallID: "c1", Content: imageLoadedMark("/gone.png")},
	}}
	if err := h.OnLoop(context.Background(), state); err != nil {
		t.Fatalf("onloop: %v", err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("missing file should not insert, got %d", len(state.Messages))
	}
	if !strings.Contains(state.Messages[1].Content, "加载失败") {
		t.Fatalf("content = %q", state.Messages[1].Content)
	}
}

func mustJSON(s string) []byte { return []byte(s) }
