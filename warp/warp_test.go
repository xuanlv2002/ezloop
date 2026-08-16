package warp

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/provider"
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

type taggedProvider struct {
	tag   string
	inner provider.ModelProvider
	tags  *[]string
}

func (p taggedProvider) Invoke(ctx context.Context, req *types.ModelRequest) (*types.ModelResponse, error) {
	*p.tags = append(*p.tags, "enter:"+p.tag)
	resp, err := p.inner.Invoke(ctx, req)
	*p.tags = append(*p.tags, "exit:"+p.tag)
	return resp, err
}

// 先注册的位于最外层；空链透传。
func TestWarpModelChain(t *testing.T) {
	var tags []string
	inner := tagProvider{tag: "core", tags: &tags}
	w1 := func(_ event.Emitter, inner provider.ModelProvider) provider.ModelProvider {
		return taggedProvider{tag: "w1", inner: inner, tags: &tags}
	}
	w2 := func(_ event.Emitter, inner provider.ModelProvider) provider.ModelProvider {
		return taggedProvider{tag: "w2", inner: inner, tags: &tags}
	}
	p := Model(nil, inner, w1, w2)
	if _, err := p.Invoke(context.Background(), &types.ModelRequest{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	want := []string{"enter:w1", "enter:w2", "core", "exit:w2", "exit:w1"}
	if !reflect.DeepEqual(tags, want) {
		t.Fatalf("order: %v", tags)
	}

	var tags2 []string
	p2 := Model(nil, tagProvider{tag: "core", tags: &tags2})
	if _, err := p2.Invoke(context.Background(), &types.ModelRequest{}); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !reflect.DeepEqual(tags2, []string{"core"}) {
		t.Fatalf("empty chain: %v", tags2)
	}
}

// Emitter 注入：链上每个工厂收到的都是引擎传入的同一出口。
func TestWarpEmitterInjection(t *testing.T) {
	em := event.Emitter(func(_ event.EventType, _ any) {})
	var received []event.Emitter
	h := func(_ event.Emitter, inner provider.ModelProvider) provider.ModelProvider {
		return inner
	}
	collect := func(got event.Emitter, inner provider.ModelProvider) provider.ModelProvider {
		received = append(received, got)
		return inner
	}
	Model(em, tagProvider{}, h, collect, collect)
	if len(received) != 2 {
		t.Fatalf("received: %d", len(received))
	}
	want := reflect.ValueOf(em).Pointer()
	for _, r := range received {
		if reflect.ValueOf(r).Pointer() != want {
			t.Fatal("every handler must receive the engine-provided emitter")
		}
	}
}

// Tool 链与 Model 链行为一致。
type echoTool struct{ tags *[]string }

func (e echoTool) Name() string                { return "echo" }
func (e echoTool) Description() string         { return "" }
func (e echoTool) ArgsSchema() json.RawMessage { return nil }
func (e echoTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	*e.tags = append(*e.tags, "core")
	return "ok", nil
}

func TestToolChain(t *testing.T) {
	var tags []string
	tt := Tool(nil, echoTool{tags: &tags}, func(_ event.Emitter, inner types.Tool) types.Tool {
		return tagTool{prefix: "w1", inner: inner, tags: &tags}
	})
	if _, err := tt.Invoke(context.Background(), nil); err != nil {
		t.Fatalf("err: %v", err)
	}
	if !reflect.DeepEqual(tags, []string{"w1", "core"}) {
		t.Fatalf("tool chain: %v", tags)
	}
}

type tagTool struct {
	prefix string
	inner  types.Tool
	tags   *[]string
}

func (t tagTool) Name() string                { return t.inner.Name() }
func (t tagTool) Description() string         { return t.inner.Description() }
func (t tagTool) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }
func (t tagTool) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	*t.tags = append(*t.tags, t.prefix)
	return t.inner.Invoke(ctx, args)
}
