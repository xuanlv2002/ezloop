/*
Package provutil 提供 provider 实现的公共薄层：结构化 HTTP 错误
（modelretry 等装饰器按 Retryable() 判断可重试性，不解析错误文本）、
状态检查与 SSE 行扫描器。协议结构各包自持，这里只收敛跨协议
易漂移的错误语义与流读取细节。
*/
package provutil

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

/* HTTPError 是非 2xx 响应的结构化错误；Proto 用于错误前缀（"openai" 等）。 */
type HTTPError struct {
	Proto  string
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s: http %d: %s", e.Proto, e.Status, e.Body)
}

/*
Retryable：408（请求超时）、429（限流）与 5xx 可安全重试；

其余 4xx（鉴权错误、请求格式错误等）重试无意义。
*/
func (e *HTTPError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout ||
		e.Status == http.StatusTooManyRequests ||
		e.Status >= http.StatusInternalServerError
}

/* CheckStatus 检查响应状态，非 2xx 返回携带截断 body 的 *HTTPError。 */
func CheckStatus(proto string, resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return &HTTPError{Proto: proto, Status: resp.StatusCode, Body: string(body)}
	}
	return nil
}

/* SSEScanner 构造服务端推送流的行扫描器（64KB 起步、上限 1MB 的长行缓冲）。 */
func SSEScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	return sc
}

/*
SafeArgs 保证工具参数是可 marshal 的合法 JSON。流式增量逐块拼接的参数
在超长输出下可能损坏（缺块/乱块/截断），非法字节直接进 json.RawMessage
会连锁炸掉三处：sessionstore 落盘 marshal 失败（整轮静默丢失）、
openai 协议重发请求被上游 400、前端工具卡乱码。非法时截断预览包成
{"_corrupted_args": "..."}——工具以缺参报错回传，模型自纠重发。
*/
func SafeArgs(raw []byte) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage("{}")
	}
	if json.Valid(raw) {
		return json.RawMessage(raw)
	}
	preview := string(raw)
	if len(preview) > 1024 {
		preview = preview[:1024] + "…(truncated)"
	}
	esc, _ := json.Marshal(preview)
	return json.RawMessage(`{"_corrupted_args":` + string(esc) + `}`)
}
