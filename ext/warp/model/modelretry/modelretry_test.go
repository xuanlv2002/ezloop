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
	failures int
	calls    int
	chunks   []string
}

func (p *flakyProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	if p.calls <= p.failures {
		return nil, errors.New("transient")
	}
	return &types.ModelResponse{Content: "ok"}, nil
}

func (p *flakyProvider) Stream(ctx context.Context, req *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	resp, err := p.Invoke(ctx, req)
	if err != nil {
		return nil, err
	}
	for _, c := range p.chunks {
		_ = onChunk(types.ModelChunk{ContentDelta: c})
	}
	return resp, nil
}

func fastOpts() func(*Options) {
	return func(o *Options) { o.BaseDelay = time.Millisecond }
}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	fp := &flakyProvider{failures: 2}
	p := New(fp, fastOpts())
	resp, err := p.Invoke(context.Background(), &types.ModelRequest{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Content != "ok" || fp.calls != 3 {
		t.Fatalf("resp=%q calls=%d", resp.Content, fp.calls)
	}
}

func TestRetryGivesUp(t *testing.T) {
	fp := &flakyProvider{failures: 100}
	p := New(fp, fastOpts())
	_, err := p.Invoke(context.Background(), &types.ModelRequest{})
	if err == nil || !strings.Contains(err.Error(), "giving up") {
		t.Fatalf("want give-up error, got %v", err)
	}
	if fp.calls != 3 {
		t.Fatalf("calls: %d", fp.calls)
	}
}

func TestRetryIfFilters(t *testing.T) {
	fp := &fatalProvider{}
	p := New(fp, fastOpts(), func(o *Options) {
		o.RetryIf = func(err error) bool { return !strings.Contains(err.Error(), "fatal") }
	})
	_, _ = p.Invoke(context.Background(), &types.ModelRequest{})
	if fp.calls != 1 {
		t.Fatalf("fatal error must not retry, calls=%d", fp.calls)
	}
}

type fatalProvider struct{ calls int }

func (p *fatalProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	p.calls++
	return nil, errors.New("fatal: bad request")
}

func TestStreamRetryBeforeFirstChunk(t *testing.T) {
	fp := &flakyProvider{failures: 1, chunks: []string{"a", "b"}}
	p := New(fp, fastOpts())
	var got []string
	resp, err := p.Stream(context.Background(), &types.ModelRequest{}, func(c types.ModelChunk) error {
		got = append(got, c.ContentDelta)
		return nil
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Content != "ok" || len(got) != 2 {
		t.Fatalf("resp=%q chunks=%v", resp.Content, got)
	}
	if fp.calls != 2 {
		t.Fatalf("want retry before first chunk, calls=%d", fp.calls)
	}
}

// streamFailAfterChunk 发出一个 chunk 后失败。
type streamFailAfterChunk struct{ calls int }

func (p *streamFailAfterChunk) Invoke(context.Context, *types.ModelRequest) (*types.ModelResponse, error) {
	return &types.ModelResponse{}, nil
}

func (p *streamFailAfterChunk) Stream(_ context.Context, _ *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	p.calls++
	_ = onChunk(types.ModelChunk{ContentDelta: "partial"})
	return nil, errors.New("connection reset")
}

func TestStreamNoRetryAfterChunkEmitted(t *testing.T) {
	fp := &streamFailAfterChunk{}
	p := New(fp, fastOpts())
	_, err := p.Stream(context.Background(), &types.ModelRequest{}, func(types.ModelChunk) error { return nil })
	if err == nil {
		t.Fatal("want error")
	}
	if fp.calls != 1 {
		t.Fatalf("must not retry after chunk emitted, calls=%d", fp.calls)
	}
}
