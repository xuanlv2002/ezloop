/*
ezloop 完整 agent 示例：集成框架全部能力。

能力清单：

	Provider   openai（.env / 环境变量配置，SiliconFlow/DeepSeek/Ollama 兼容）
	           流式接收正文与思考过程（reasoning_content，推理模型可见）
	Warp       modelretry（模型重试）· limit（工具并发闸）· safetool（panic 防护）
	Hook       filetools（read/write/edit/grep/find + bash）
	           approve（工具审批）· askuser（模型提问）· taskplan（规划确认）
	           —— 三者同构：channel 决策中断，CLI 桥接 stdin
	           task（并行分身：fork 当前 Agent 干子任务，过程隔离、结果回传，
	             事件与 session 按 forkID 区分，分身内审批/提问照常工作）
	           skill（从 skills/*.md 按需注入）
	           localsession（会话持久化，/resume 恢复；分身写独立 session 可回放）
	交互       流式输出（正文+思考）· 分身输出带 ⟨task-N⟩ 标记 · Ctrl+C 取消当轮
	命令       /new /sessions /resume <id> /summary（手动摘要）/exit

运行：cp .env.example .env && go run ./examples/chat
*/
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xuanlv2002/ezloop/ext/provider/openai"
	"github.com/xuanlv2002/ezloop/types"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadDotEnv() {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k == "" || os.Getenv(k) != "" {
			continue
		}
		_ = os.Setenv(k, v)
	}
}

func main() {
	loadDotEnv()
	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Println("请先配置 OPENAI_API_KEY：复制 .env.example 为 .env 填入，或设置环境变量")
		os.Exit(1)
	}

	// ── 组装：全部 hook / warp / 超参 ──
	p := openai.New(openai.Options{
		BaseURL: env("OPENAI_BASE_URL", "https://api.siliconflow.cn/v1"),
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   env("EZLOOP_MODEL", "deepseek-ai/DeepSeek-V3.2"),
	})
	resp, _ := p.Stream(context.Background(), &types.ModelRequest{
		Messages: []types.Message{
			{Role: "system", Content: "你是一个资深的 Go 语言专家，精通 ezloop 框架。"},
			{Role: "user", Content: "你好"},
		}}, func(c types.ModelChunk) error {
		fmt.Println(c)
		return nil
	},
	)
	fmt.Println(resp)
}
