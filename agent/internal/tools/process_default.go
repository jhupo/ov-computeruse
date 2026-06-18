//go:build !windows

package tools

import (
	"context"
	"os/exec"
	"syscall"
)

func prepareCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func startCommand(cmd *exec.Cmd) error {
	return cmd.Start()
}

func waitCommand(ctx context.Context, cmd *exec.Cmd) error {
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Wait() }()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		killCommandTree(cmd)
		err := <-errCh
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}

func killCommandTree(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	_ = cmd.Process.Kill()
}
