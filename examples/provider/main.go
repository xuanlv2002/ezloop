/*
ezloop provider 示例：三种输入形态直连模型并打印响应 JSON（非流式 Invoke）。

	text   纯文本输入
	image  纯图片输入（程序生成的 64×64 PNG：红底中央蓝圆，无外部依赖）
	mixed  文本 + 图片输入

每个请求都带一个简单工具（get_time），便于观察端点行为：
输出走 content 还是 reasoning_content、是否触发 tool_calls。

运行：cp .env.example .env 填 OPENAI_API_KEY（默认 SiliconFlow 端点），
go run ./examples/provider [text|image|mixed]（缺省跑全部）。
*/
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strings"
	"time"

	"github.com/xuanlv2002/ezloop/ext/provider/openai"
	"github.com/xuanlv2002/ezloop/provider"
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

/* timeTool 是最小工具实现：模型可主动调用获取当前时间。 */
type timeTool struct{}

func (timeTool) Name() string        { return "get_time" }
func (timeTool) Description() string { return "获取本机当前时间" }
func (timeTool) ArgsSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (timeTool) Invoke(_ context.Context, _ json.RawMessage) (string, error) {
	return time.Now().Format("2006-01-02 15:04:05"), nil
}

/* testPNG 生成 64×64 测试图：红底中央蓝圆。 */
func testPNG() types.ImagePart {
	const size = 64
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			dx, dy := float64(x)-size/2+0.5, float64(y)-size/2+0.5
			if math.Sqrt(dx*dx+dy*dy) < 16 {
				img.Set(x, y, color.RGBA{B: 255, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 255, A: 255})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return types.ImagePart{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString(buf.Bytes())}
}

/* userMsg 组装输入：可选文本 + 可选图片。 */
func userMsg(text string, withImage bool) types.Message {
	m := types.Message{Role: types.RoleUser, Content: text}
	if withImage {
		m.Images = []types.ImagePart{testPNG()}
	}
	return m
}

func main() {
	loadDotEnv()
	if os.Getenv("OPENAI_API_KEY") == "" {
		fmt.Println("请先配置 OPENAI_API_KEY：复制 .env.example 为 .env 填入，或设置环境变量")
		os.Exit(1)
	}
	p := openai.New(openai.Options{
		BaseURL: env("OPENAI_BASE_URL", "https://api.siliconflow.cn/v1"),
		APIKey:  os.Getenv("OPENAI_API_KEY"),
		Model:   env("EZLOOP_MODEL", "Qwen/Qwen3-Omni-30B-A3B-Instruct"),
	})

	cases := []struct {
		name string
		text string
		img  bool
	}{
		{"text", "用一句话介绍你自己。", false},
		{"image", "", true},
		{"mixed", "图中是什么颜色和形状？一句话回答。", true},
	}
	which := "all"
	if len(os.Args) > 1 {
		which = os.Args[1]
	}
	for _, c := range cases {
		if which != "all" && which != c.name {
			continue
		}
		req := &types.ModelRequest{
			Messages: []types.Message{userMsg(c.text, c.img)},
			Tools:    []types.Tool{timeTool{}},
		}
		fmt.Printf("\n===== [%s] 文本=%t 图片=%t 工具=get_time =====\n", c.name, c.text != "", c.img)
		run(p, req)
	}
}

func run(p provider.ModelProvider, req *types.ModelRequest) {
	resp, err := p.Invoke(context.Background(), req)
	if err != nil {
		fmt.Println("错误:", err)
		return
	}
	b, err := json.MarshalIndent(resp, "", "  ")
	if err != nil {
		fmt.Println("序列化失败:", err)
		return
	}
	fmt.Println(string(b))
}
