// Package localsession 将 session 以文件形式持久化到本地文件系统：
// EndHook 时把完整可恢复状态（消息历史、用量、停止原因）写入
// sessions/<id>.json，同一 ID 每轮滚动覆盖为最新快照。
// 恢复用 Load 取出历史，经 core.WithHistory 继续对话。
package localsession

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/types"
)

// DefaultDir 是会话文件在 FS 内的默认目录。
const DefaultDir = "sessions"

// Session 是一次会话的可持久化快照。
type Session struct {
	ID         string          `json:"id"`
	Input      string          `json:"input"`
	Messages   []types.Message `json:"messages"`
	Iterations int             `json:"iterations"`
	StopReason string          `json:"stop_reason"`
	StartedAt  time.Time       `json:"started_at"`
	EndedAt    time.Time       `json:"ended_at"`
}

// Hook 在 EndHook 时持久化会话快照。
type Hook struct {
	fsys fs.FileSystem
	dir  string
	id   string
}

// New 创建会话持久化 hook。id 为空时自动生成；
// 每轮结束后调用 SetID 可切换到已有会话继续（滚动覆盖同一文件）。
func New(fsys fs.FileSystem, id string, opts ...func(*Hook)) *Hook {
	if id == "" {
		id = NewID()
	}
	h := &Hook{fsys: fsys, dir: DefaultDir, id: id}
	for _, fn := range opts {
		fn(h)
	}
	return h
}

// WithDir 自定义会话目录（FS 内路径）。
func WithDir(dir string) func(*Hook) {
	return func(h *Hook) {
		if dir != "" {
			h.dir = strings.Trim(dir, "/")
		}
	}
}

// ID 返回当前会话 ID。
func (h *Hook) ID() string { return h.id }

// SetID 切换会话（后续轮次写入新 ID 的文件）。
func (h *Hook) SetID(id string) {
	if id != "" {
		h.id = id
	}
}

func (h *Hook) Name() string { return "localsession" }

// Path 返回当前会话的存储路径。
func (h *Hook) Path() string { return h.dir + "/" + h.id + ".json" }

// OnEnd 持久化快照；失败不阻断主流程（写入错误记入 Metadata）。
func (h *Hook) OnEnd(_ context.Context, state *types.LoopState) error {
	snap := Session{
		ID:         h.id,
		Input:      state.Input,
		Messages:   state.Messages,
		Iterations: state.Iteration,
		StopReason: string(state.StopReason),
		StartedAt:  state.StartedAt,
		EndedAt:    state.EndedAt,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		state.Metadata["localsession_error"] = err.Error()
		return nil
	}
	if err := h.fsys.Write(context.Background(), h.Path(), data); err != nil {
		state.Metadata["localsession_error"] = err.Error()
	}
	return nil
}

// NewID 生成短随机会话 ID。
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// Load 读取指定会话。
func Load(ctx context.Context, fsys fs.FileSystem, dir, id string) (*Session, error) {
	if dir == "" {
		dir = DefaultDir
	}
	data, err := fsys.Read(ctx, dir+"/"+id+".json")
	if err != nil {
		return nil, fmt.Errorf("localsession: load %s: %w", id, err)
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("localsession: decode %s: %w", id, err)
	}
	return &s, nil
}

// List 返回全部会话 ID（按文件名排序）。
func List(ctx context.Context, fsys fs.FileSystem, dir string) ([]string, error) {
	if dir == "" {
		dir = DefaultDir
	}
	entries, err := fsys.List(ctx, dir)
	if err != nil {
		return nil, nil // 目录不存在视为无会话
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir && strings.HasSuffix(e.Name, ".json") {
			ids = append(ids, strings.TrimSuffix(e.Name, ".json"))
		}
	}
	return ids, nil
}
