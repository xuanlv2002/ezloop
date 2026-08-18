package types

import (
	"context"
	"encoding/json"
	"sort"
	"testing"
)

type stubTool string

func (s stubTool) Name() string                { return string(s) }
func (s stubTool) Description() string         { return "" }
func (s stubTool) ArgsSchema() json.RawMessage { return nil }
func (s stubTool) Invoke(context.Context, json.RawMessage) (string, error) {
	return "", nil
}

// List 按工具名排序——顺序是工具集的确定函数,与注册顺序、组装路径
// 无关(resume、重新 New 都不打散),前缀缓存(KV cache)才有命中的前提。
// 重复注册同名工具不重复占位。
func TestToolRegistryListSortedByName(t *testing.T) {
	names := []string{"task", "read", "bash", "write", "edit", "grep", "find", "now"}
	r := NewToolRegistry()
	for _, n := range names { // 乱序注册
		r.Register(stubTool(n))
	}
	r.Register(stubTool("edit")) // 重注册:覆盖但不重复占位

	want := append([]string(nil), names...)
	sort.Strings(want)
	for range 50 {
		got := r.List()
		if len(got) != len(want) {
			t.Fatalf("len=%d want=%d", len(got), len(want))
		}
		for j, tool := range got {
			if tool.Name() != want[j] {
				t.Fatalf("want %s at %d, got %s", want[j], j, tool.Name())
			}
		}
	}
}
