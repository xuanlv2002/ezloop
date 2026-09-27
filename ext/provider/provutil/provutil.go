/*
Package provutil 提供 provider 实现的公共薄层：结构化 HTTP 错误
（modelretry 等装饰器按 Retryable() 判断可重试性，不解析错误文本）、
状态检查与 SSE 行扫描器。协议结构各包自持，这里只收敛跨协议
易漂移的错误语义与流读取细节。
*/
package provutil

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"
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
IdleWatch 是流式空闲看门狗：连续 idle 无任何数据（断流/网关挂死）时
cancel 请求 ctx（读 body 随即断出），数据持续到达则不限总时长（超长
思考合法）。响应到达后 Touch 一次，此后每收到一批数据（scanner.Scan
返回 true）Touch 续期；流读完后 Stop。客户端响应没有读 deadline 的
官方 API，以 cancel 实现。
*/
type IdleWatch struct {
	cancel   context.CancelFunc
	idle     time.Duration
	timedOut atomic.Bool
	ch       chan struct{}
}

/* NewIdleWatch 挂载看门狗并返回其派生 ctx（idle<=0 时为无操作看门狗）。 */
func NewIdleWatch(ctx context.Context, idle time.Duration) (*IdleWatch, context.Context) {
	if idle <= 0 {
		return &IdleWatch{}, ctx
	}
	cctx, cancel := context.WithCancel(ctx)
	w := &IdleWatch{cancel: cancel, idle: idle, ch: make(chan struct{}, 1)}
	go w.loop(cctx)
	return w, cctx
}

/* Touch 续期（非阻塞；看门狗未激活时无操作）。 */
func (w *IdleWatch) Touch() {
	if w.ch == nil {
		return
	}
	select {
	case w.ch <- struct{}{}:
	default:
	}
}

/* Stop 停看门狗（流读完后调用，正常/错误路径都要；幂等）。 */
func (w *IdleWatch) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
}

/* TimedOut 报告是否因空闲超时触发（区别于调用方主动 cancel）。 */
func (w *IdleWatch) TimedOut() bool { return w.timedOut.Load() }

func (w *IdleWatch) loop(ctx context.Context) {
	timer := time.NewTimer(w.idle)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.ch:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.idle)
		case <-timer.C:
			w.timedOut.Store(true)
			w.cancel()
			return
		}
	}
}

/*
WrapStreamErr 包装流读收尾错误。空闲超时触发时错误标记为
os.ErrDeadlineExceeded 并注明无数据时长——不透传 context.Canceled
（modelretry 会把 Canceled 当用户取消而放弃重试）。三协议 provider
的 scanner.Err() 统一走这。
*/
func WrapStreamErr(proto string, err error, w *IdleWatch, idle time.Duration) error {
	if w.TimedOut() {
		return fmt.Errorf("%s: read stream: no data for %s (idle timeout): %w", proto, idle, os.ErrDeadlineExceeded)
	}
	return fmt.Errorf("%s: read stream: %w", proto, err)
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
