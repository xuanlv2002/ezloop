package modelretry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
