package codexcli

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

func Available() bool {
	_, err := ResolveBin("")
	return err == nil
}

func ResolveBin(configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured), nil
	}
	for _, candidate := range binCandidates(runtime.GOOS) {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("codex CLI executable not found")
}

func binCandidates(goos string) []string {
	if goos == "windows" {
		return []string{"codex.exe", "codex.cmd", "codex"}
	}
	return []string{"codex"}
}
