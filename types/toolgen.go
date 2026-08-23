package types

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

/*
genTool 是 NewTool 构造的工具：schema 在构造期由反射生成一次，之后不可变，
多 goroutine 并发调 ArgsSchema / Invoke 无竞态。
*/
type genTool[A any] struct {
	name   string
	desc   string
	schema json.RawMessage
	fn     func(ctx context.Context, in *A) (string, error)
}

/*
NewTool 从函数构造工具：A 是参数结构体，schema 由其 struct tag 反射生成——
json:"name,omitempty" 定字段名（omitempty → 非 required，默认全 required）、
desc:"…" → description、enum:"a|b|c" → string enum。支持 string / bool /
number（int*、uint*、float*）/ slice / map[string]T / json.RawMessage / 嵌套
struct；不支持的类型构造期 panic（组装者错误，fail fast，优于运行期产出
畸形 schema）。properties 与 required 均按字段声明序输出——schema 是 A 的
确定函数，跨进程稳定，KV cache 前缀才可能命中（不得改用 json.Marshal(map)，
其按 key 排序会打散声明序）。

required 只约束字段是否必须出现在 JSON 里，不做非零校验：允许空串的字段
（如 edit_file 的 new_text 删除语义）照常 required，值级业务校验留在 fn 内。
*/
func NewTool[A any](name, desc string, fn func(ctx context.Context, in *A) (string, error)) Tool {
	return &genTool[A]{
		name:   name,
		desc:   desc,
		schema: json.RawMessage(schemaFor(reflect.TypeFor[A]())),
		fn:     fn,
	}
}

func (t *genTool[A]) Name() string                { return t.name }
func (t *genTool[A]) Description() string         { return t.desc }
func (t *genTool[A]) ArgsSchema() json.RawMessage { return t.schema }
func (t *genTool[A]) Invoke(ctx context.Context, args json.RawMessage) (string, error) {
	var in A
	if err := json.Unmarshal(args, &in); err != nil {
		// 错误作为工具结果回传模型自纠，不终止 loop（项目错误语义）。
		return "", fmt.Errorf("%s: invalid args: %w", t.name, err)
	}
	return t.fn(ctx, &in)
}

/* schemaFor 按 t 的字段声明序生成最小 JSON Schema；非 struct 直接 panic。 */
func schemaFor(t reflect.Type) string {
	if t.Kind() != reflect.Struct {
		panic("toolgen: args must be a struct, got " + t.String())
	}
	var b strings.Builder
	var required []string
	b.WriteString(`{"type":"object","properties":{`)
	first := true
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, skip := fieldName(f)
		if skip {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(quoteJSON(name))
		b.WriteByte(':')
		b.WriteString(fieldSchema(f.Type, f.Tag))
		if _, omitempty := parseJSONTag(f.Tag); !omitempty {
			required = append(required, name)
		}
	}
	b.WriteString(`}`)
	if len(required) > 0 {
		b.WriteString(`,"required":[`)
		for i, n := range required {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(quoteJSON(n))
		}
		b.WriteByte(']')
	}
	b.WriteString(`}`)
	return b.String()
}

/* fieldName 解析字段的 JSON 名；未导出或 json:"-" 跳过。 */
func fieldName(f reflect.StructField) (name string, skip bool) {
	if f.PkgPath != "" {
		return "", true
	}
	name, _ = parseJSONTag(f.Tag)
	if name == "-" {
		return "", true
	}
	return name, false
}

/* parseJSONTag 返回 json tag 的名字与 omitempty 标记。 */
func parseJSONTag(tag reflect.StructTag) (name string, omitempty bool) {
	parts := strings.Split(tag.Get("json"), ",")
	name = parts[0]
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			omitempty = true
		}
	}
	return name, omitempty
}

/*
fieldSchema 生成单个字段的 schema 片段：typeSchema 出类型部分，desc / enum
tag 叠加在其后。enum 校验先于类型生成——落在非 string 字段上是组装者
笔误，panic，静默忽略会让模型收到与实现不符的 schema。
*/
func fieldSchema(t reflect.Type, tag reflect.StructTag) string {
	if t.Kind() == reflect.Pointer {
		return fieldSchema(t.Elem(), tag)
	}
	var extras []string
	if e := tag.Get("enum"); e != "" {
		if t.Kind() != reflect.String {
			panic("toolgen: enum tag is only valid on string fields, got " + t.String())
		}
		vals := make([]string, 0, strings.Count(e, "|")+1)
		for v := range strings.SplitSeq(e, "|") {
			vals = append(vals, quoteJSON(v))
		}
		extras = append(extras, `,"enum":[`+strings.Join(vals, ",")+`]`)
	}
	if d := tag.Get("desc"); d != "" {
		extras = append(extras, `,"description":`+quoteJSON(d))
	}
	base := typeSchema(t)
	if len(extras) == 0 {
		return base
	}
	return base[:len(base)-1] + strings.Join(extras, "") + `}`
}

/*
typeSchema 生成类型的 schema 片段。[]byte 与 json.RawMessage 单列：
encoding/json 把它们序列化为字符串或任意 JSON，不能落进 slice 的
items:number 分支骗模型。
*/
func typeSchema(t reflect.Type) string {
	switch {
	case t == reflect.TypeFor[json.RawMessage]():
		return `{"type":"object"}`
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8:
		return `{"type":"string"}`
	case t.Kind() == reflect.Struct:
		return schemaFor(t)
	case t.Kind() == reflect.String:
		return `{"type":"string"}`
	case t.Kind() == reflect.Bool:
		return `{"type":"boolean"}`
	case isNumberKind(t.Kind()):
		return `{"type":"number"}`
	case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
		return `{"type":"array","items":` + typeSchema(t.Elem()) + `}`
	case t.Kind() == reflect.Map:
		if t.Key().Kind() != reflect.String {
			panic("toolgen: map key must be string, got " + t.String())
		}
		return `{"type":"object"}`
	default:
		panic("toolgen: unsupported field type " + t.String())
	}
}

func isNumberKind(k reflect.Kind) bool {
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
