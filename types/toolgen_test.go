package types

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type genFull struct {
	Name   string          `json:"name" desc:"名称"`
	Mode   string          `json:"mode" enum:"a|b|c" desc:"模式"`
	On     bool            `json:"on"`
	Count  int64           `json:"count"`
	Ratio  float64         `json:"ratio"`
	Note   string          `json:"note,omitempty" desc:"备注"`
	Tags   []string        `json:"tags,omitempty"`
	Meta   map[string]any  `json:"meta,omitempty"`
	Any    json.RawMessage `json:"any,omitempty"`
	Sub    genSub          `json:"sub"`
	Skip   string          `json:"-"`
	hidden string
}

type genSub struct {
	X string `json:"x"`
}

func TestSchemaForFull(t *testing.T) {
	got := NewTool("t", "d", func(context.Context, *genFull) (string, error) { return "", nil }).ArgsSchema()
	want := `{"type":"object","properties":{` +
		`"name":{"type":"string","description":"名称"},` +
		`"mode":{"type":"string","enum":["a","b","c"],"description":"模式"},` +
		`"on":{"type":"boolean"},` +
		`"count":{"type":"number"},` +
		`"ratio":{"type":"number"},` +
		`"note":{"type":"string","description":"备注"},` +
		`"tags":{"type":"array","items":{"type":"string"}},` +
		`"meta":{"type":"object"},` +
		`"any":{"type":"object"},` +
		`"sub":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}` +
		`},"required":["name","mode","on","count","ratio","sub"]}`
	if string(got) != want {
		t.Fatalf("schema mismatch:\n got: %s\nwant: %s", got, want)
	}
}

// 空参数工具：无 required key，properties 为空对象。
func TestSchemaForEmpty(t *testing.T) {
	got := NewTool("t", "d", func(_ context.Context, _ *struct{}) (string, error) { return "", nil }).ArgsSchema()
	if want := `{"type":"object","properties":{}}`; string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// 全 omitempty：省略整个 required key；未导出与 json:"-" 字段不出现。
func TestSchemaForAllOptional(t *testing.T) {
	type allOpt struct {
		A string `json:"a,omitempty"`
		B bool   `json:"b,omitempty"`
	}
	got := NewTool("t", "d", func(context.Context, *allOpt) (string, error) { return "", nil }).ArgsSchema()
	want := `{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"boolean"}}}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

// 不支持的类型是组装者错误：构造期 panic，fail fast。
func TestSchemaForPanics(t *testing.T) {
	type badChan struct {
		C chan int `json:"c"`
	}
	type badEnum struct {
		N int `json:"n" enum:"a|b"`
	}
	for _, fn := range []func(){
		func() { _ = NewTool("t", "d", func(context.Context, *badChan) (string, error) { return "", nil }) },
		func() { _ = NewTool("t", "d", func(context.Context, *badEnum) (string, error) { return "", nil }) },
	} {
		func() {
			defer func() {
				r := recover()
				if r == nil || !strings.Contains(r.(string), "toolgen") {
					t.Fatalf("want toolgen panic, got %v", r)
				}
			}()
			fn()
		}()
	}
}

func TestGenToolInvoke(t *testing.T) {
	tool := NewTool("echo", "回显", func(_ context.Context, in *genSub) (string, error) {
		return in.X, nil
	})
	if _, err := tool.Invoke(context.Background(), json.RawMessage(`{"x":"hi"}`)); err != nil {
		t.Fatalf("invoke: %v", err)
	}
	// 非法 JSON：错误带工具名回传模型自纠。
	if _, err := tool.Invoke(context.Background(), json.RawMessage(`{bad`)); err == nil ||
		!strings.Contains(err.Error(), "echo") {
		t.Fatalf("want named error, got %v", err)
	}
	// fn 自身的 error 原样透传。
	boom := NewTool("boom", "d", func(context.Context, *genSub) (string, error) {
		return "", errors.New("boom failed")
	})
	if _, err := boom.Invoke(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("want fn error passthrough")
	}
}

// schema 构造期生成一次后不可变：并发调 ArgsSchema / Invoke 无竞态（-race 钉子）。
func TestGenToolConcurrent(t *testing.T) {
	tool := NewTool("t", "d", func(_ context.Context, in *genSub) (string, error) {
		return in.X, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = tool.ArgsSchema()
				_, _ = tool.Invoke(context.Background(), json.RawMessage(`{"x":"a"}`))
			}
		}()
	}
	wg.Wait()
}
