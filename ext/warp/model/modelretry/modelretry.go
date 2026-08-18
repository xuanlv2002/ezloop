/*
Package modelretry 是 provider 装饰器：对模型调用做指数退避重试。
注意它是 provider 包装而非 hook——引擎在模型节点失败即终止 loop，
hook 拦截不到模型调用本身，重试必须发生在 provider 内部。
*/
package modelretry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
	"github.com/xuanlv2002/ezloop/warp"
)

/*
EventRetry 是重试事件：Data 为 *RetryInfo。
经引擎组装时注入的 event.Emitter 流出，
前端可渲染「第 N 次失败，Xms 后重试」。
*/
const EventRetry = event.EventType("modelretry.retry")

/* RetryInfo 描述一次即将进行的重试。 */
type RetryInfo struct {
	Attempt     int // 即将开始的尝试序号（2 = 第二次尝试）
	MaxAttempts int
	Delay       time.Duration
	Err         error // 上一次尝试的失败原因
}

type Options struct {
	// MaxAttempts 总尝试次数（含首次），默认 3。
	MaxAttempts int
	// BaseDelay 首次重试前等待，默认 500ms，之后指数翻倍。
	BaseDelay time.Duration
	// RetryIf 决定一个错误是否重试，可按错误类型/文本/状态码任意匹配。
	// 默认策略（见 defaultRetryable）：
	//   - context 取消不重试；
	//   - 错误（或其包装）实现了 Retryable() bool 接口（如 openai.HTTPError）按其判断；
	//   - 其余无法识别的错误保守重试。
	RetryIf func(err error) bool
}

type RetryProvider struct {
	inner provider.ModelProvider
	opts  Options
	em    event.Emitter // 引擎组装时注入，可为 nil（直接 New 构造时）
}

/* New 返回包装后的 provider；若 inner 支持 Stream 则包装后同样支持。 */
func New(p provider.ModelProvider, opts ...func(*Options)) *RetryProvider {
	o := Options{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond}
	for _, fn := range opts {
		fn(&o)
	}
	if o.RetryIf == nil {
		o.RetryIf = defaultRetryable
	}
	return &RetryProvider{inner: p, opts: o}
}

/*
retryable 是与 openai.HTTPError 等结构化错误的解耦契约：
provider 层实现它，本包无需感知具体错误类型。
*/
type retryable interface{ Retryable() bool }

func defaultRetryable(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	var r retryable
	if errors.As(err, &r) {
		return r.Retryable()
	}
	return true
}

var _ provider.ModelProvider = (*RetryProvider)(nil)
var _ provider.StreamProvider = (*RetryProvider)(nil)

/*
Warp 返回可传入 core.WithModelWarp 的中间件形式，
引擎组装时注入 per-Run 事件出口（重试事件经它发出）。
*/
func Warp(opts ...func(*Options)) warp.ModelHandler {
	return func(em event.Emitter, p provider.ModelProvider) provider.ModelProvider {
		r := New(p, opts...)
		r.em = em
		return r
	}
}

func (r *RetryProvider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	var lastErr error
	for attempt := 0; attempt < r.opts.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := r.backoff(ctx, attempt, lastErr); err != nil {
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

/* Stream 重试整个流式调用；一旦已有 chunk 透出便不再重试（避免重复输出）。 */
func (r *RetryProvider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	sp, ok := r.inner.(provider.StreamProvider)
	if !ok {
		// 与引擎的降级检测同款事件：降级不静默。
		if r.em != nil {
			r.em(event.EventStreamFallback, "modelretry: inner provider does not implement StreamProvider; falling back to Invoke")
		}
		return r.Invoke(ctx, req)
	}
	var lastErr error
	for attempt := 0; attempt < r.opts.MaxAttempts; attempt++ {
		if attempt > 0 {
			if err := r.backoff(ctx, attempt, lastErr); err != nil {
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

/* backoff 发出重试事件后等待退避时间；cause 是上一次尝试的失败原因。 */
func (r *RetryProvider) backoff(ctx context.Context, attempt int, cause error) error {
	delay := r.opts.BaseDelay << (attempt - 1)
	// 加 20% 抖动，避免并发重试形成共振。
	delay += time.Duration(float64(delay) * 0.2 * rand.Float64())
	if r.em != nil {
		r.em(EventRetry, &RetryInfo{
			Attempt:     attempt + 1,
			MaxAttempts: r.opts.MaxAttempts,
			Delay:       delay,
			Err:         cause,
		})
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
