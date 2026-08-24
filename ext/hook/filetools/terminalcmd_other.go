//go:build !windows

package filetools

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

func terminalCmd(ctx context.Context, raw, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", raw)
	cmd.Dir = dir
	// 独立进程组 + 取消时杀整组：只杀 sh 的话管道写端仍在孙进程手里，
	// Wait 会阻塞到孙进程退出（停止无效）；组杀让管道立即关闭。
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
	return cmd
}
