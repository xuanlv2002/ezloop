// Package warperror 是 hook/tool 的健壮性装饰器：
// panic 恢复为 error、error 附加上下文（hook 名 / 工具名），
// 避免单个扩展的崩溃炸掉整个 agent loop。
package warperror

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xuanlv2002/ezloop/hook"
	"github.com/xuanlv2002/ezloop/types"
)

// Wrap 装饰任意 hook：转发全部 7 个阶段，panic 与 error 均被包装。
func Wrap(h hook.Hook) hook.Hook { return &wrappedHook{inner: h} }

type wrappedHook struct{ inner hook.Hook }

func (w *wrappedHook) Name() string { return w.inner.Name() }

func (w *wrappedHook) OnStart(ctx context.Context, state *types.LoopState) (err error) {
	if s, ok := w.inner.(hook.StartHook); ok {
		defer w.recoverTo(&err, "OnStart")
		return wrapErr(w.inner, s.OnStart(ctx, state))
	}
	return nil
}

func (w *wrappedHook) OnModelStart(ctx context.Context, state *types.LoopState) (err error) {
	if s, ok := w.inner.(hook.ModelStartHook); ok {
		defer w.recoverTo(&err, "OnModelStart")
		return wrapErr(w.inner, s.OnModelStart(ctx, state))
	}
	return nil
}

func (w *wrappedHook) OnModelEnd(ctx context.Context, state *types.LoopState) (err error) {
	if s, ok := w.inner.(hook.ModelEndHook); ok {
		defer w.recoverTo(&err, "OnModelEnd")
		return wrapErr(w.inner, s.OnModelEnd(ctx, state))
	}
	return nil
}

func (w *wrappedHook) OnToolStart(ctx context.Context, state *types.LoopState, call *types.ToolCall) (action hook.Action, err error) {
	if s, ok := w.inner.(hook.ToolStartHook); ok {
		defer func() {
			if r := recover(); r != nil {
				action, err = hook.ActionProceed, fmt.Errorf("hook %q panicked in OnToolStart: %v", w.inner.Name(), r)
			}
		}()
		action, err = s.OnToolStart(ctx, state, call)
		if err != nil {
			err = wrapErr(w.inner, err)
		}
		return action, err
	}
	return hook.ActionProceed, nil
}

func (w *wrappedHook) OnToolEnd(ctx context.Context, state *types.LoopState, result *types.ToolResult) (err error) {
	if s, ok := w.inner.(hook.ToolEndHook); ok {
		defer w.recoverTo(&err, "OnToolEnd")
		return wrapErr(w.inner, s.OnToolEnd(ctx, state, result))
	}
	return nil
}

func (w *wrappedHook) OnLoop(ctx context.Context, state *types.LoopState) (err error) {
	if s, ok := w.inner.(hook.LoopHook); ok {
		defer w.recoverTo(&err, "OnLoop")
		return wrapErr(w.inner, s.OnLoop(ctx, state))
	}
	return nil
}

func (w *wrappedHook) OnEnd(ctx context.Context, state *types.LoopState) (err error) {
	if s, ok := w.inner.(hook.EndHook); ok {
		defer w.recoverTo(&err, "OnEnd")
		return wrapErr(w.inner, s.OnEnd(ctx, state))
	}
	return nil
}

func (w *wrappedHook) recoverTo(errp *error, phase string) {
	if r := recover(); r != nil {
		*errp = fmt.Errorf("hook %q panicked in %s: %v", w.inner.Name(), phase, r)
	}
}

func wrapErr(h hook.Hook, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("hook %q: %w", h.Name(), err)
}

// WrapTool 装饰工具：panic 恢复为 error，error 附带工具名。
// 工具错误不会终止 loop（引擎会回传模型自纠），这里只保证不崩溃、信息可定位。
func WrapTool(t types.Tool) types.Tool { return &wrappedTool{inner: t} }

type wrappedTool struct{ inner types.Tool }

func (w *wrappedTool) Name() string            { return w.inner.Name() }
func (w *wrappedTool) Description() string     { return w.inner.Description() }
func (w *wrappedTool) ArgsSchema() json.RawMessage { return w.inner.ArgsSchema() }

func (w *wrappedTool) Invoke(ctx context.Context, args json.RawMessage) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %q panicked: %v", w.inner.Name(), r)
		}
	}()
	result, err = w.inner.Invoke(ctx, args)
	if err != nil {
		err = fmt.Errorf("tool %q: %w", w.inner.Name(), err)
	}
	return result, err
}
