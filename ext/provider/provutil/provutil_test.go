package provutil

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Retryable 语义：408/429/5xx 可重试，其余 4xx 不可。
func TestHTTPErrorRetryable(t *testing.T) {
	cases := []struct {
		status int
		want   bool
	}{
		{http.StatusRequestTimeout, true},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
		{http.StatusBadRequest, false},
		{http.StatusUnauthorized, false},
		{http.StatusForbidden, false},
		{http.StatusNotFound, false},
	}
	for _, c := range cases {
		e := &HTTPError{Proto: "x", Status: c.status, Body: "x"}
		if e.Retryable() != c.want {
			t.Fatalf("status %d: want %v", c.status, c.want)
		}
	}
}

// CheckStatus 返回结构化 HTTPError，装饰器可 errors.As 断言。
func TestCheckStatusReturnsHTTPError(t *testing.T) {
	resp := &http.Response{StatusCode: 429, Body: http.NoBody}
	err := CheckStatus("openai", resp)
	if err == nil {
		t.Fatal("want error")
	}
	he, ok := err.(*HTTPError)
	if !ok || he.Status != 429 || he.Proto != "openai" {
		t.Fatalf("type=%T err=%v", err, err)
	}
}

// SafeArgs 语义：空→{}；合法→原样；非法→包成合法对象（预览截断）。
func TestSafeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", `{}`},
		{"valid object kept", `{"path":"a.txt"}`, `{"path":"a.txt"}`},
		{"valid non-object kept", `[1,2]`, `[1,2]`},
		{"truncated json wrapped", `{"path":"a.txt`, `{"_corrupted_args":"{\"path\":\"a.txt"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SafeArgs([]byte(c.in))
			if !json.Valid(got) {
				t.Fatalf("SafeArgs(%q) = %q, not valid JSON", c.in, got)
			}
			if string(got) != c.want {
				t.Fatalf("SafeArgs(%q) = %s, want %s", c.in, got, c.want)
			}
		})
	}
}

func TestSafeArgsTruncatesPreview(t *testing.T) {
	raw := `{"path":"` + strings.Repeat("x", 5000)
	got := SafeArgs([]byte(raw))
	if !json.Valid(got) {
		t.Fatalf("not valid JSON: %s", got)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	s, _ := m["_corrupted_args"].(string)
	if len(s) > 1100 {
		t.Fatalf("preview not truncated: %d chars", len(s))
	}
	if !strings.HasSuffix(s, "…(truncated)") {
		t.Fatalf("preview missing truncation marker")
	}
}

// 事故形态回归：非法 RawMessage 经 SafeArgs 后混入消息，marshal 不炸。
func TestSafeArgsRoundTripsThroughMarshal(t *testing.T) {
	msg := struct {
		Args json.RawMessage `json:"args"`
	}{Args: SafeArgs([]byte(`{"content":"半截`))}
	if _, err := json.Marshal(msg); err != nil {
		t.Fatalf("marshal: %v", err)
	}
}
