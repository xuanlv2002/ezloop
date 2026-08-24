package core

import (
	"context"
	"testing"
	"time"

	"github.com/xuanlv2002/ezloop/provider"
	"github.com/xuanlv2002/ezloop/types"
)

/* halfStreamProvider 流出两段正文后挂起，直到 ctx 取消——模拟生成中途被打断。 */
type halfStreamProvider struct{}

func (halfStreamProvider) Invoke(_ context.Context, _ *types.ModelRequest) (*types.ModelResponse, error) {
	return &types.ModelResponse{Content: "done"}, nil
}

func (halfStreamProvider) Stream(ctx context.Context, _ *types.ModelRequest, onChunk provider.ModelChunkHandler) (*types.ModelResponse, error) {
	_ = onChunk(types.ModelChunk{ContentDelta: "半截"})
	_ = onChunk(types.ModelChunk{ContentDelta: "输出"})
	<-ctx.Done()
	return nil, ctx.Err()
}

/*
取消抢救：流式中途取消时，已流出内容以 assistant 消息入史——
用户看过的话不能在下轮消失；StopReason 仍为 cancelled。
*/
func TestCancelSavesPartialStream(t *testing.T) {
	a := NewAgent(halfStreamProvider{}, WithStreaming(true))
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	state, err := a.Run(ctx, "hi")
	if err == nil {
		t.Fatal("expected cancel error")
	}
	if state.StopReason != types.StopCancelled {
		t.Fatalf("stop reason: %v", state.StopReason)
	}
	last := state.Messages[len(state.Messages)-1]
	if last.Role != types.RoleAssistant || last.Content != "半截输出" {
		t.Fatalf("partial not saved: %+v", last)
	}
}
