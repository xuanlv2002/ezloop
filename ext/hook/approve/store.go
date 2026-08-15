package approve

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/xuanlv2002/ezloop/types"
)

// Store 是审批记录：批准按 (工具名, args 指纹) 粒度保存，
// 用于轮次式审批——第 1 轮拦截、用户批准后第 2 轮放行。
type Store struct {
	mu        sync.RWMutex
	approved  map[string]struct{}
	consumeOn bool // 命中即消费（approve once 语义）
}

func NewStore(consumeOnHit bool) *Store {
	return &Store{approved: make(map[string]struct{}), consumeOn: consumeOnHit}
}

// Approve 记录一次批准。args 相同才会命中；args 变了需重新审批。
func (s *Store) Approve(tool string, args json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.approved[fingerprint(tool, args)] = struct{}{}
}

func (s *Store) IsApproved(tool string, args json.RawMessage) bool {
	key := fingerprint(tool, args)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.approved[key]; !ok {
		return false
	}
	if s.consumeOn {
		delete(s.approved, key)
	}
	return true
}

// Approver 返回接好线的 Approver：命中记录放行，否则拦截（ActionSkip 由 Hook 处理）。
func (s *Store) Approver() Approver {
	return func(_ context.Context, call *types.ToolCall) (bool, error) {
		return s.IsApproved(call.Name, call.Args), nil
	}
}

func fingerprint(tool string, args json.RawMessage) string {
	h := sha256.Sum256(append([]byte(tool+"\x00"), args...))
	return hex.EncodeToString(h[:8])
}
