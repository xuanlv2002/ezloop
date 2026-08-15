// Package modelretry 是 provider 装饰器：对模型调用做指数退避重试。
// 注意它是 provider 包装而非 hook——引擎在模型节点失败即终止 loop，
// hook 拦截不到模型调用本身，重试必须发生在 provider 内部。
package modelretry

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

type Options struct {
	// MaxAttempts 总尝试次数（含首次），默认 3。
	MaxAttempts int
	// BaseDelay 首次重试前等待，默认 500ms，之后指数翻倍。
	BaseDelay time.Duration
	// RetryIf 可选，默认所有 error 都重试。
	RetryIf func(err error) bool
}

type RetryProvider struct {
	inner provider.ModelProvider
	opts  Options
}

// New 返回包装后的 provider；若 inner 支持 Stream 则包装后同样支持。
func New(p provider.ModelProvider, opts ...func(*Options)) *RetryProvider {
	o := Options{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond}
	for _, fn := range opts {
		fn(&o)
	}
	if o.RetryIf == nil {
		o.RetryIf = func(error) bool { return true }
	}
	return &RetryProvider{inner: p, opts: o}
}

var _ provider.ModelProvider = (*RetryProvider)(nil)
var _ provider.StreamProvider = (*RetryProvider)(nil)

// Warp 返回可传入 core.WithWarp 的中间件形式。
func Warp(opts ...func(*Options)) provider.WarpHandler {
	return func(p provider.ModelProvider) provider.ModelProvider {
		return New(p, opts...)
	}
}

func (r *RetryProvider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	var lastErr error
	for attempt := 0; attempt < r.opts.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := r.backoff(ctx, attempt); err != nil {
				return nil, err
			}
		}
		resp, err := r.inner.Invoke(ctx, req)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if !r.opts.RetryIf(err) {
			break
		}
	}
	return nil, fmt.Errorf("modelretry: giving up after %d attempts: %w", r.opts.MaxAttempts, lastErr)
}

// Stream 重试整个流式调用；一旦已有 chunk 透出便不再重试（避免重复输出）。
func (r *RetryProvider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	sp, ok := r.inner.(provider.StreamProvider)
	if !ok {
		return r.Invoke(ctx, req)
	}
	var lastErr error
	for attempt := 0; attempt < r.opts.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := r.backoff(ctx, attempt); err != nil {
				return nil, err
			}
		}
		emitted := false
		resp, err := sp.Stream(ctx, req, func(c types.ModelChunk) error {
			emitted = true
			return onChunk(c)
		})
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if emitted || !r.opts.RetryIf(err) {
			break
		}
	}
	return nil, fmt.Errorf("modelretry: giving up: %w", lastErr)
}

func (r *RetryProvider) backoff(ctx context.Context, attempt int) error {
	delay := r.opts.BaseDelay << (attempt - 1)
	// 加 20% 抖动，避免并发重试形成共振。
	delay += time.Duration(float64(delay) * 0.2 * rand.Float64())
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
