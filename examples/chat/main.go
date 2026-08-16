// ezloop 完整 agent 示例：集成框架全部能力。
//
// 能力清单：
//   Provider   openai（.env / 环境变量配置，SiliconFlow/DeepSeek/Ollama 兼容）
//   Warp       modelretry（模型重试）· safetool（panic 防护）· offload（大结果卸载）
//   Hook       filetools（read/write/edit/grep/find + bash，CLI 审批）
//              skill（从 skills/*.md 按需注入）· summary（每轮结束自动摘要）
//              localsession（会话持久化，/resume 恢复）
//   交互       流式输出 · 工具执行前 y/n 确认（中断） · Ctrl+C 取消当轮
//   命令       /new /sessions /resume <id> /exit
//
// 运行：cp .env.example .env && go run ./examples/chat
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/ext/hook/approve"
	"github.com/xuanlv2002/ezloop/ext/hook/filetools"
	"github.com/xuanlv2002/ezloop/ext/hook/localsession"
	"github.com/xuanlv2002/ezloop/ext/hook/skill"
	"github.com/xuanlv2002/ezloop/ext/hook/summary"
	"github.com/xuanlv2002/ezloop/ext/provider/openai"
	"github.com/xuanlv2002/ezloop/ext/warp/model/modelretry"
	"github.com/xuanlv2002/ezloop/ext/warp/tool/offload"
	"github.com/xuanlv2002/ezloop/ext/warp/tool/safetool"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// ── 工作区文件系统（沙箱 = 当前目录，路径穿越被拒绝）──
	fsys := fs.NewLocal(".")

	// ── 会话持久化 ──
	session := localsession.New(fsys, "")

	// ── 技能：从 skills/*.md 加载（目录不存在则空集）──
	skillHook, err := skill.NewFromFS(ctx, fsys, "skills")
	if err != nil {
		skillHook = skill.New()
	}

	// ── CLI 审批：写操作与命令执行需要 y/n 确认，只读操作放行 ──
	scanner := bufio.NewScanner(os.Stdin)
	approver := approve.New(func(_ context.Context, call *types.ToolCall) (bool, error) {
		switch call.Name {
		case "read_file", "list_dir", "grep", "find", "now":
			return true, nil
		}
		fmt.Printf("\n⚠️  工具 %s 请求执行: %s\n   放行? [y/N] ", call.Name, string(call.Args))
		if !scanner.Scan() {
			return false, nil
		}
		return strings.EqualFold(strings.TrimSpace(scanner.Text()), "y"), nil
	})

	// ── 组装：全部 hook / warp / 超参 ──
	p := openai.New(openai.Options{
		BaseURL: env("OPENAI_BASE_URL", "https://api.siliconflow.cn/v1"),
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   env("EZLOOP_MODEL", "deepseek-ai/DeepSeek-V3.2"),
	})
	agent := core.NewAgent(p,
		core.WithSystemPrompt("你是 ezloop 驱动的命令行助手。能用工具就用工具，回答简洁。"),
		core.WithModelWarp(modelretry.Warp()),
		core.WithToolWarp(safetool.Warp(), offload.Warp(fsys)),
		core.WithHooks(
			filetools.New(fsys, func(o *filetools.Options) { o.EnableExec = true }),
			skillHook,
			approver,
			summary.New(p, ""),
			session,
		),
		core.WithTools(nowTool{}),
		core.WithHyperParams(core.HyperParams{MaxIterations: 12, MaxConcurrency: 4}),
		core.WithStreaming(true),
		core.WithOnEvent(func(e event.Event) {
			switch e.Type {
			case event.EventModelChunk:
				fmt.Print(e.Data.(string))
			case event.EventToolStart:
				fmt.Printf("\n🔧 %s ", e.Data.(*types.ToolCall).Name)
			case event.EventIterationEnd:
				fmt.Printf("\n── 迭代 %d ──\n", e.Iteration)
			}
		}),
	)

	fmt.Println("ezloop 完整 agent 已启动（Ctrl+C 取消当轮；命令：/new /sessions /resume <id> /exit）")
	fmt.Printf("会话 ID: %s\n", session.ID())
	var history []types.Message

	for {
		fmt.Print("\n你: ")
		if !scanner.Scan() {
			if serr := scanner.Err(); serr != nil {
				fmt.Println("\n[输入错误]", serr)
			}
			return
		}
		input := strings.TrimSpace(scanner.Text())
		switch {
		case input == "" :
			continue
		case input == "/exit" || input == "/quit":
			return
		case input == "/new":
			session.SetID(localsession.NewID())
			history = nil
			fmt.Println("已开启新会话:", session.ID())
			continue
		case input == "/sessions":
			ids, _ := localsession.List(ctx, fsys, "")
			fmt.Println("历史会话:", strings.Join(ids, ", "))
			continue
		case strings.HasPrefix(input, "/resume "):
			id := strings.TrimSpace(strings.TrimPrefix(input, "/resume "))
			s, err := localsession.Load(ctx, fsys, "", id)
			if err != nil {
				fmt.Println("恢复失败:", err)
				continue
			}
			session.SetID(id)
			history = s.Messages
			fmt.Printf("已恢复会话 %s（%d 条消息）\n", id, len(s.Messages))
			continue
		}

		// Ctrl+C 只取消当轮：换一个可取消的子 ctx，主循环继续。
		turnCtx, cancelTurn := context.WithCancel(ctx)
		go func() {
			<-ctx.Done()
			cancelTurn()
		}()

		fmt.Print("助手: ")
		state, err := agent.Run(turnCtx, input, core.WithHistory(history...))
		if err != nil {
			fmt.Printf("\n[本轮结束: %v]\n", err)
			cancelTurn()
			continue
		}
		if ctx.Err() != nil { // Ctrl+C 退出程序
			return
		}
		cancelTurn()

		history = state.Messages
		fmt.Printf("\n[%s · %d 迭代 · 会话 %s]", state.StopReason, state.Iteration, session.ID())
		if s, ok := state.Metadata["summary"].(string); ok && s != "" {
			fmt.Printf("\n📝 摘要: %s", s)
		}
		if e := state.Metadata["localsession_error"]; e != nil {
			fmt.Printf("\n[会话保存失败: %v]", e)
		}
		fmt.Println()
	}
}

// nowTool 获取当前时间。
type nowTool struct{}

func (nowTool) Name() string        { return "now" }
func (nowTool) Description() string { return "获取当前本地时间" }
func (nowTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (nowTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return time.Now().Format("2006-01-02 15:04:05 MST"), nil
}
