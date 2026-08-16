// Package safetool 是工具节点中间件：panic 恢复为 error、error 附带工具名，
// 单个工具的崩溃不会炸掉整个 agent loop（工具错误由引擎回传模型自纠）。
package safetool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/types"
	"github.com/xuanlv2002/ezloop/warp"
)

// Warp 返回工具中间件（Emitter 保留给需要发事件的装饰器）。
func Warp() warp.ToolHandler {
	return func(_ event.Emitter, inner types.Tool) types.Tool {
		return &safeTool{inner: inner}
	}
}

type safeTool struct{ inner types.Tool }

func (t *safeTool) Name() string                { return t.inner.Name() }
func (t *safeTool) Description() string         { return t.inner.Description() }
func (t *safeTool) ArgsSchema() json.RawMessage { return t.inner.ArgsSchema() }

func (t *safeTool) Invoke(ctx context.Context, args json.RawMessage) (result string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tool %q panicked: %v", t.inner.Name(), r)
		}
	}()
	result, err = t.inner.Invoke(ctx, args)
	if err != nil {
		err = fmt.Errorf("tool %q: %w", t.inner.Name(), err)
	}
	return result, err
}
