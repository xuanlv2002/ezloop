// ezloop 完整 agent 示例：集成框架全部能力。
//
// 能力清单：
//
//	Provider   openai（.env / 环境变量配置，SiliconFlow/DeepSeek/Ollama 兼容）
//	Warp       modelretry（模型重试）· safetool（panic 防护）· offload（大结果卸载）
//	Hook       filetools（read/write/edit/grep/find + bash）
//	           approve（工具审批）· askuser（模型提问）· taskplan（规划确认）
//	           —— 三者同构：channel 决策中断，CLI 桥接 stdin
//	           skill（从 skills/*.md 按需注入）· summary（每轮结束自动摘要）
//	           localsession（会话持久化，/resume 恢复）
//	交互       流式输出 · Ctrl+C 取消当轮
//	命令       /new /sessions /resume <id> /exit
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
	"sync"
	"syscall"
	"time"

	"github.com/xuanlv2002/ezloop/core"
	"github.com/xuanlv2002/ezloop/event"
	"github.com/xuanlv2002/ezloop/ext/fs"
	"github.com/xuanlv2002/ezloop/ext/hook/approve"
	"github.com/xuanlv2002/ezloop/ext/hook/askuser"
	"github.com/xuanlv2002/ezloop/ext/hook/contextfix"
	"github.com/xuanlv2002/ezloop/ext/hook/filetools"
	"github.com/xuanlv2002/ezloop/ext/hook/localsession"
	"github.com/xuanlv2002/ezloop/ext/hook/offload"
	"github.com/xuanlv2002/ezloop/ext/hook/skill"
	"github.com/xuanlv2002/ezloop/ext/hook/summary"
	"github.com/xuanlv2002/ezloop/ext/hook/taskplan"
	"github.com/xuanlv2002/ezloop/ext/provider/openai"
	"github.com/xuanlv2002/ezloop/ext/warp/model/modelretry"
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

	// ── 人机交互 hook：审批 / 提问 / 规划（channel 决策模式）──
	// hook 在工具调用前阻塞等决策；CLI 桥在 OnEvent 里呈现请求、
	// 读 stdin 后从独立 goroutine 回传 channel（同步回传会死锁）。
	scanner := bufio.NewScanner(os.Stdin)
	approver, approveCh := approve.New(func(c *types.ToolCall) bool {
		switch c.Name {
		case "read_file", "list_dir", "grep", "find", "now", askuser.ToolName, taskplan.ToolName:
			return false // 只读工具与人机交互工具免审
		case "bash":
			// 参数值级判断：白名单只读命令免审，其余（写/删/网络）需确认
			var a struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(c.Args, &a)
			for _, p := range []string{"ls", "cat", "head", "tail", "pwd", "git status", "git diff", "git log", "go test"} {
				if a.Command == p || strings.HasPrefix(a.Command, p+" ") {
					return false
				}
			}
		}
		return true
	})
	asker, answerCh := askuser.New()
	planner, planCh := taskplan.New()

	// 判定段并发：多个审批请求会同时到达，CLI 桥用锁串行化提问
	// （Web 场景则是并发展示卡片、批量点击后各自回传）。
	var askMu sync.Mutex
	ask := func(prompt string) string {
		askMu.Lock()
		defer askMu.Unlock()
		fmt.Print(prompt)
		if !scanner.Scan() {
			return ""
		}
		return strings.TrimSpace(scanner.Text())
	}

	// ── 组装：全部 hook / warp / 超参 ──
	p := openai.New(openai.Options{
		BaseURL: env("OPENAI_BASE_URL", "https://api.siliconflow.cn/v1"),
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   env("EZLOOP_MODEL", "deepseek-ai/DeepSeek-V3.2"),
	})
	agent := core.NewAgent(p,
		core.WithSystemPrompt("你是 ezloop 驱动的命令行助手。能用工具就用工具，回答简洁。"),
		core.WithModelWarp(modelretry.Warp()),
		core.WithToolWarp(safetool.Warp()),
		core.WithHooks(
			contextfix.New(), // 历史进入引擎前先修理（/resume 旧存档防悬空 tool_call）
			offload.New(fsys),
			filetools.New(fsys, func(o *filetools.Options) { o.EnableExec = true }),
			skillHook,
			approver,
			asker,
			planner,
			summary.New(p, ""),
			session,
		),
		core.WithTools(nowTool{}),
		core.WithHyperParams(core.HyperParams{MaxIterations: 12}),
		core.WithStreaming(true),
		core.WithOnEvent(func(e event.Event) {
			switch e.Type {
			case event.EventModelChunk:
				fmt.Print(e.Data.(string))
			case event.EventToolStart:
				fmt.Printf("\n🔧 %s ", e.Data.(*types.ToolCall).Name)
			case event.EventIterationEnd:
				fmt.Printf("\n── 迭代 %d ──\n", e.Iteration)
			case approve.EventRequest:
				call := e.Data.(*types.ToolCall)
				go func() { // 判定段并发，CLI 桥用 askMu 串行化提问
					in := ask(fmt.Sprintf("\n⚠️  工具 %s 请求执行: %s\n   放行? [y/N] ", call.Name, string(call.Args)))
					send(ctx, approveCh, approve.Decision{CallID: call.ID, Approve: strings.EqualFold(in, "y")})
				}()
			case askuser.EventRequest:
				call := e.Data.(*types.ToolCall)
				var q struct {
					Question string `json:"question"`
				}
				_ = json.Unmarshal(call.Args, &q)
				go func() {
					send(ctx, answerCh, askuser.Answer{CallID: call.ID, Input: ask("\n❓ " + q.Question + "\n你: ")})
				}()
			case taskplan.EventRequest:
				call := e.Data.(*types.ToolCall)
				var p struct {
					Plan string `json:"plan"`
				}
				_ = json.Unmarshal(call.Args, &p)
				go func() {
					in := ask("\n📋 规划提交:\n" + p.Plan + "\n[e]执行 [n]否决, 其他输入=修改意见\n你: ")
					d := taskplan.Decision{CallID: call.ID, Kind: taskplan.Revise, Input: in}
					switch in {
					case "e":
						d = taskplan.Decision{CallID: call.ID, Kind: taskplan.Execute}
					case "n":
						d = taskplan.Decision{CallID: call.ID, Kind: taskplan.Reject}
					}
					send(ctx, planCh, d)
				}()
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
		case input == "":
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

// send 向决策 channel 发送，程序退出时不悬挂。
func send[T any](ctx context.Context, ch chan<- T, v T) {
	select {
	case ch <- v:
	case <-ctx.Done():
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
