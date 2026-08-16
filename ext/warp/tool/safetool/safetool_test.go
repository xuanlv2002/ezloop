package safetool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type panicTool struct{}

func (panicTool) Name() string                { return "boom" }
func (panicTool) Description() string         { return "" }
func (panicTool) ArgsSchema() json.RawMessage { return nil }
func (panicTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	panic("tool crash")
}

func TestWarpRecoversPanic(t *testing.T) {
	_, err := Warp()(nil, panicTool{}).Invoke(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err: %v", err)
	}
}
