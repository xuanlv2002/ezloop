package filetools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/xuanlv2002/ezloop/ext/fs"
)

/* 临时验证：用户实测报错的 7 条引号命令逐条过一遍真实 terminal 工具。 */
func TestManualQuoteCommands(t *testing.T) {
	dir := t.TempDir()
	hook := New(fs.NewLocal(dir), WithWorkDir(dir))

	cmds := []string{
		`systeminfo | findstr /C:"OS"`,
		`ipconfig | findstr /i "IPv4"`,
		`cmd /c "ipconfig > ip.txt"`,
		`type ip.txt | findstr /n "."`,
		`ipconfig | findstr /C:"IPv4 Address"`,
		`ipconfig | findstr "IPv4"`,
		`powershell -Command "ipconfig | Select-String 'IPv4'"`,
		`powershell -Command ipconfig | Select-String IPv4`,
	}
	for _, c := range cmds {
		args, _ := json.Marshal(map[string]string{"command": c})
		out := runTool(t, hook, "terminal", string(args))
		t.Logf("CMD: %s\nOUT: %s\n----", c, out)
	}
	_ = context.Background
}
