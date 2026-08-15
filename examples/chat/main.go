// 一个真实的最小 agent：OpenAI 兼容接口 + 多轮对话 + 一个时间工具 + 流式输出。
//
// 配置（环境变量，均可选，有默认值）：
//
//	OPENAI_BASE_URL  默认 https://api.openai.com/v1（可指向 DeepSeek/Ollama 等）
//	OPENAI_API_KEY   必填
//	  Windows:   set OPENAI_API_KEY=sk-xxx
//	EZLOOP_MODEL     默认 gpt-4o-mini
//
// 运行：go run ./examples/chat
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/provider/openai"
	"github.com/xuanlv2002/ezloop/ext/warp/model/modelretry"
	"github.com/xuanlv2002/ezloop/types"
)

// timeTool 告诉模型现在几点，让工具循环有真实用武之地。
type timeTool struct{}

func (timeTool) Name() string        { return "now" }
func (timeTool) Description() string { return "获取当前本地时间" }
func (timeTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (timeTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return time.Now().Format("2006-01-02 15:04:05 MST"), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")

	if apiKey == "" {
		fmt.Println("请先设置 OPENAI_API_KEY（或修改代码里的 BaseURL 指向本地 Ollama 等）")
		os.Exit(1)
	}

	p := openai.New(openai.Options{
		BaseURL: env("OPENAI_BASE_URL", "https://api.siliconflow.cn/v1"),
		APIKey:  apiKey,
		Model:   env("EZLOOP_MODEL", "deepseek-ai/DeepSeek-V3.2"),
	})

	agent := core.NewAgent(p,
		core.WithSystemPrompt("你是 ezloop 驱动的命令行助手，回答简洁。需要知道时间就调用工具。"),
		core.WithModelWarp(modelretry.Warp()),
		core.WithTools(timeTool{}),
		core.WithHyperParams(core.HyperParams{MaxIterations: 8, MaxConcurrency: 4}),
		core.WithStreaming(true),
		// 流式增量直接打到终端；其余事件只挑关键的提示。
		core.WithOnEvent(func(e event.Event) {
			switch e.Type {
			case event.EventModelChunk:
				fmt.Print(e.Data.(string))
			case event.EventToolStart:
				call := e.Data.(*types.ToolCall)
				fmt.Printf("\n[调用工具 %s] ", call.Name)
			case event.EventToolEnd:
				fmt.Println()
			}
		}),
	)

	fmt.Println("ezloop chat 已启动（输入 exit 退出）")
	scanner := bufio.NewScanner(os.Stdin)
	var history []types.Message

	for {
		fmt.Print("\n你: ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				fmt.Println("\n[输入错误]", err)
			}
			return
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}
		if input == "exit" || input == "quit" {
			return
		}

		fmt.Print("助手: ")
		state, err := agent.Run(context.Background(), input, core.WithHistory(history...))
		if err != nil {
			fmt.Println("\n[出错]", err)
			continue
		}
		fmt.Println()
		// 全量历史（含本轮工具消息）带入下一轮。
		history = state.Messages
	}
}
