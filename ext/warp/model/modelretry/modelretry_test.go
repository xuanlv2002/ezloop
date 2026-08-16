package modelretry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

type flakyProvider struct {
	failures, calls int
}

func (p *flakyProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	if p.calls <= p.failures {
		return nil, errors.New("transient")
	}
	return &types.ModelResponse{Content: "ok"}, nil
}

func fast() func(*Options) { return func(o *Options) { o.BaseDelay = time.Millisecond } }

// 瞬时失败重试后成功；持续失败按 MaxAttempts 放弃。
func TestRetryAndGiveUp(t *testing.T) {
	fp := &flakyProvider{failures: 2}
	resp, err := New(fp, fast()).Invoke(context.Background(), &types.ModelRequest{})
	if err != nil || resp.Content != "ok" || fp.calls != 3 {
		t.Fatalf("retry: resp=%q calls=%d err=%v", resp.Content, fp.calls, err)
	}

	fp2 := &flakyProvider{failures: 100}
	_, err = New(fp2, fast()).Invoke(context.Background(), &types.ModelRequest{})
	if err == nil || !strings.Contains(err.Error(), "giving up") || fp2.calls != 3 {
		t.Fatalf("give up: calls=%d err=%v", fp2.calls, err)
	}
}

// 流式：首个 chunk 之前可重试，之后失败不再重试（避免重复输出）。
type streamFailAfterChunk struct{ calls int }

func (p *streamFailAfterChunk) Invoke(context.Context, *types.ModelRequest) (*types.ModelResponse, error) {
	return &types.ModelResponse{}, nil
}
func (p *streamFailAfterChunk) Stream(_ context.Context, _ *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	p.calls++
	_ = onChunk(types.ModelChunk{ContentDelta: "partial"})
	return nil, errors.New("reset")
}

func TestStreamNoRetryAfterChunk(t *testing.T) {
	fp := &streamFailAfterChunk{}
	_, err := New(fp, fast()).Stream(context.Background(), &types.ModelRequest{}, func(types.ModelChunk) error { return nil })
	if err == nil || fp.calls != 1 {
		t.Fatalf("calls=%d err=%v", fp.calls, err)
	}
}

type statusErr struct {
	retryable bool
}

func (e *statusErr) Error() string   { return "http error" }
func (e *statusErr) Retryable() bool { return e.retryable }

type stubProvider struct {
	calls int
	err   error
}

func (p *stubProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	return nil, p.err
}

// 默认策略：Retryable()==false 只试一次；==true 按 MaxAttempts 重试；取消不重试。
func TestDefaultRetryablePolicy(t *testing.T) {
	noret := &stubProvider{err: &statusErr{retryable: false}}
	_, _ = New(noret, fast()).Invoke(context.Background(), &types.ModelRequest{})
	if noret.calls != 1 {
		t.Fatalf("non-retryable must not retry: calls=%d", noret.calls)
	}

	ret := &stubProvider{err: &statusErr{retryable: true}}
	_, _ = New(ret, fast()).Invoke(context.Background(), &types.ModelRequest{})
	if ret.calls != 3 {
		t.Fatalf("retryable must retry: calls=%d", ret.calls)
	}

	canceled := &stubProvider{err: context.Canceled}
	_, _ = New(canceled, fast()).Invoke(context.Background(), &types.ModelRequest{})
	if canceled.calls != 1 {
		t.Fatalf("canceled must not retry: calls=%d", canceled.calls)
	}

	// 用户自定义 RetryIf 覆盖默认（错误文本匹配示例）。
	custom := &stubProvider{err: errors.New("billing hard-exceeded")}
	_, _ = New(custom, fast(), func(o *Options) {
		o.RetryIf = func(err error) bool { return !strings.Contains(err.Error(), "hard-exceeded") }
	}).Invoke(context.Background(), &types.ModelRequest{})
	if custom.calls != 1 {
		t.Fatalf("custom RetryIf must be honored: calls=%d", custom.calls)
	}
}

// 重试事件经 ctx 出口流出：每次重试一条，含尝试序号与失败原因。
func TestRetryEvents(t *testing.T) {
	fp := &flakyProvider{failures: 2}
	var mu sync.Mutex
	var infos []*RetryInfo
	ctx := event.ContextWithEmitter(context.Background(), func(e event.Event) {
		mu.Lock()
		defer mu.Unlock()
		if e.Type == EventRetry {
			infos = append(infos, e.Data.(*RetryInfo))
		}
	})
	resp, err := New(fp, fast()).Invoke(ctx, &types.ModelRequest{})
	if err != nil || resp.Content != "ok" {
		t.Fatalf("err=%v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("retry events: %d", len(infos))
	}
	if infos[0].Attempt != 2 || infos[1].Attempt != 3 {
		t.Fatalf("attempts: %d %d", infos[0].Attempt, infos[1].Attempt)
	}
	if infos[0].Err == nil || infos[0].Delay <= 0 {
		t.Fatalf("info: %+v", infos[0])
	}
}
