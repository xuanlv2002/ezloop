// Package await 提供决策路由器：人机交互 hook（approve/askuser/taskplan）
// 的多个等待者共享同一个决策 channel，并发判定下投递到谁 是随机的——
// Router 按 key（CallID）把值路由到正确的等待者：错配的暂存并广播唤醒，
// 等待者重查暂存。直接丢弃错配值会造成互饿死锁。
package await

import (
	"context"
	"sync"
)

// Router 把共享 channel 的并发投递按 key 路由到各自的等待者。
// key 返回空串的值视为通配：由收到它的等待者直接消费。
type Router[T any] struct {
	ch   <-chan T
	key  func(T) string
	mu   sync.Mutex
	pend map[string]T
	wake chan struct{}
}

func New[T any](ch <-chan T, key func(T) string) *Router[T] {
	return &Router[T]{ch: ch, key: key, pend: map[string]T{}, wake: make(chan struct{})}
}

// Await 阻塞直到 key 匹配 id 的值到达或 ctx 取消（ok=false）。
func (r *Router[T]) Await(ctx context.Context, id string) (v T, ok bool) {
	for {
		if v, ok = r.take(id); ok {
			return v, true
		}
		r.mu.Lock()
		wake := r.wake
		r.mu.Unlock()
		select {
		case d := <-r.ch:
			if k := r.key(d); k == "" || k == id {
				return d, true
			}
			r.stash(d)
		case <-wake: // 暂存更新，回循环头重查
		case <-ctx.Done():
			return v, false
		}
	}
}

func (r *Router[T]) take(id string) (T, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.pend[id]
	if ok {
		delete(r.pend, id)
	}
	return v, ok
}

// stash 暂存错配值并广播：close 唤醒所有绑定旧 wake 的等待者，
// 它们重查暂存后绑定新 wake 继续等待。
func (r *Router[T]) stash(v T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id := r.key(v); id != "" {
		r.pend[id] = v
	}
	close(r.wake)
	r.wake = make(chan struct{})
}
