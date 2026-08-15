package core

import (
	"context"
	"testing"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

func TestWithWarpAppliesToProvider(t *testing.T) {
	var calls []string
	base := &mockProvider{script: []*types.ModelResponse{textResp("ok")}}
	warped := provider.Warp(base, func(p provider.ModelProvider) provider.ModelProvider {
		return &recordingProvider{inner: p, calls: &calls, tag: "audit"}
	})

	a := NewAgent(warped, WithMaxIterations(1))
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(calls) != 1 || calls[0] != "audit" {
		t.Fatalf("warp not applied: %v", calls)
	}
}

// WithModelWarp 直接在 NewAgent 里包装。
func TestWithModelWarpOption(t *testing.T) {
	var calls []string
	base := &mockProvider{script: []*types.ModelResponse{textResp("ok")}}

	a := NewAgent(base,
		WithMaxIterations(1),
		WithModelWarp(func(p provider.ModelProvider) provider.ModelProvider {
			return &recordingProvider{inner: p, calls: &calls, tag: "w"}
		}),
	)
	if _, err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("warp option not applied: %v", calls)
	}
}

type recordingProvider struct {
	inner provider.ModelProvider
	calls *[]string
	tag   string
}

func (p *recordingProvider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	*p.calls = append(*p.calls, p.tag)
	return p.inner.Invoke(ctx, req)
}
