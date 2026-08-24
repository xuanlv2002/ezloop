/*
Windows 构造 terminal 子进程：整条命令行经 SysProcAttr.CmdLine 原样
交给 CreateProcess，引号语义完全归 cmd 解析。不能走 exec.Args——
EscapeArg 会把命令里的双引号转义成 \"，而 cmd 不认反斜杠转义、只按
"剥首尾引号"规则处理，内部引号连同反斜杠成了字面量：dir "路径"、
findstr /C:"a b"、powershell -Command "…" 全部损坏。chcp 65001
预置在此（控制台与重定向文件转 UTF-8；审批按模型原始命令判定，
不受此包装影响）。
*/
//go:build windows

package filetools

import (
	"context"
	"os/exec"
	"syscall"
)

func terminalCmd(ctx context.Context, raw, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "cmd")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd /c chcp 65001 >nul & ` + raw}
	cmd.Dir = dir
	return cmd
}
