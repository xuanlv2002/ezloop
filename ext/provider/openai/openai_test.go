package openai

import (
	"net/http"
	"testing"
)

// Retryable 语义:408/429/5xx 可重试,其余 4xx 不可。
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
		e := &HTTPError{Status: c.status, Body: "x"}
		if e.Retryable() != c.want {
			t.Fatalf("status %d: want %v", c.status, c.want)
		}
	}
}

// checkStatus 返回结构化 HTTPError,装饰器可 errors.As 断言。
func TestCheckStatusReturnsHTTPError(t *testing.T) {
	resp := &http.Response{StatusCode: 429, Body: http.NoBody}
	err := checkStatus(resp)
	if err == nil {
		t.Fatal("want error")
	}
	he, ok := err.(*HTTPError)
	if !ok || he.Status != 429 {
		t.Fatalf("type=%T err=%v", err, err)
	}
}
