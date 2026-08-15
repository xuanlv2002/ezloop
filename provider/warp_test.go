package provider

import (
	"context"
	"testing"

	"github.com/xuanlv2002/ezloop/types"
)

type tagProvider struct {
	tag  string
	tags *[]string
}

func (p tagProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	*p.tags = append(*p.tags, p.tag)
	return &types.ModelResponse{Content: p.tag}, nil
}

type tagWarp struct {
	name string
	tags *[]string
}

func (w tagWarp) wrap(inner ModelProvider) ModelProvider {
	return taggedProvider{tag: w.name, inner: inner, tags: w.tags}
}

type taggedProvider struct {
	tag   string
	inner ModelProvider
	tags  *[]string
}

func (p taggedProvider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	*p.tags = append(*p.tags, "enter:"+p.tag)
	resp, err := p.inner.Invoke(ctx, req)
	*p.tags = append(*p.tags, "exit:"+p.tag)
	return resp, err
}

func TestWarpOrderFirstRegisteredOutermost(t *testing.T) {
	var tags []string
	inner := tagProvider{tag: "core", tags: &tags}
	p := Warp(inner,
		(tagWarp{name: "w1", tags: &tags}).wrap,
		(tagWarp{name: "w2", tags: &tags}).wrap,
	)
	if _, err := p.Invoke(context.Background(), &types.ModelRequest{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"enter:w1", "enter:w2", "core", "exit:w2", "exit:w1"}
	if len(tags) != len(want) {
		t.Fatalf("tags: %v", tags)
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("order: %v", tags)
		}
	}
}

func TestWarpEmpty(t *testing.T) {
	var tags []string
	inner := tagProvider{tag: "core", tags: &tags}
	p := Warp(inner)
	if _, err := p.Invoke(context.Background(), &types.ModelRequest{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(tags) != 1 || tags[0] != "core" {
		t.Fatalf("tags: %v", tags)
	}
}
