//go:build !windows

package tools

import (
	"context"
	"os/exec"

	"ov-computeruse/agent/internal/processtree"
)

func prepareCommand(cmd *exec.Cmd) {
	processtree.Prepare(cmd)
}

func startCommand(cmd *exec.Cmd) error {
	return processtree.Start(cmd)
}

func waitCommand(ctx context.Context, cmd *exec.Cmd) error {
	return processtree.Wait(ctx, cmd)
}

func killCommandTree(cmd *exec.Cmd) {
	processtree.Kill(cmd)
}
