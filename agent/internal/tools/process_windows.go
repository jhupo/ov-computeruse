//go:build windows

package tools

import (
	"context"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var commandJobs sync.Map

func prepareCommand(cmd *exec.Cmd) {}

func startCommand(cmd *exec.Cmd) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	)
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = cmd.Process.Kill()
		_ = windows.CloseHandle(job)
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		_ = cmd.Process.Kill()
		_ = windows.CloseHandle(job)
		return err
	}
	commandJobs.Store(cmd.Process.Pid, job)
	return nil
}

func waitCommand(ctx context.Context, cmd *exec.Cmd) error {
	errCh := make(chan error, 1)
	go func() { errCh <- cmd.Wait() }()
	select {
	case err := <-errCh:
		closeCommandJob(cmd)
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
	closeCommandJob(cmd)
	_ = cmd.Process.Kill()
}

func closeCommandJob(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if job, ok := commandJobs.LoadAndDelete(cmd.Process.Pid); ok {
		_ = windows.CloseHandle(job.(windows.Handle))
	}
}
