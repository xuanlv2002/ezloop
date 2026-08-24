//go:build !windows

package filetools

import (
	"context"
	"os/exec"
)

func terminalCmd(ctx context.Context, raw, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", raw)
	cmd.Dir = dir
	return cmd
}
