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

type failTool struct{}

func (failTool) Name() string                { return "failing" }
func (failTool) Description() string         { return "" }
func (failTool) ArgsSchema() json.RawMessage { return nil }
func (failTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return "", assertErr("disk full")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func TestWarpRecoversPanic(t *testing.T) {
	tool := Warp()(panicTool{})
	out, err := tool.Invoke(context.Background(), json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "panicked") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err: %v", err)
	}
	if out != "" {
		t.Fatalf("out: %q", out)
	}
}

func TestWarpAddsToolNameToError(t *testing.T) {
	tool := Warp()(failTool{})
	_, err := tool.Invoke(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "failing") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err: %v", err)
	}
}
