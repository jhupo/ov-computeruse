package codexcli

import (
	"context"
	"io"
	"os/exec"

	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

func (a *Adapter) exec(ctx context.Context, command protocol.Command, sink agentruntime.Sink, resume bool) error {
	prompt, err := promptFromCommand(command)
	if err != nil {
		return err
	}
	resolved, err := a.resolve(ctx, command)
	if err != nil {
		return err
	}
	bin, err := ResolveBin(a.cfg.BinPath)
	if err != nil {
		return err
	}
	args, cwd, err := a.buildArgs(command, resolved, resume)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.active.track(command, cancel)
	defer func() {
		cancel()
		a.active.untrack(command)
	}()

	cmd := exec.CommandContext(runCtx, bin, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, prompt); err != nil {
		_ = stdin.Close()
		return err
	}
	_ = stdin.Close()

	readErrs := make(chan error, 2)
	go func() { readErrs <- a.readStdout(runCtx, stdout, command, resolved, sink) }()
	go func() { readErrs <- readStderr(runCtx, stderr, command, sink) }()

	waitErr := cmd.Wait()
	for i := 0; i < 2; i++ {
		if err := <-readErrs; err != nil && waitErr == nil {
			waitErr = err
		}
	}
	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	return waitErr
}
