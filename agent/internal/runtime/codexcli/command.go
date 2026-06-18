package codexcli

import (
	"encoding/json"
	"errors"
	"strings"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
)

type commandPayload struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

func promptFromCommand(command protocol.Command) (string, error) {
	if len(command.Payload) == 0 {
		return "", errors.New("prompt payload is required")
	}
	var payload commandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return "", err
	}
	prompt := firstNonEmpty(payload.Prompt, payload.Text)
	if prompt == "" {
		return "", errors.New("prompt is required")
	}
	return prompt, nil
}

func (a *Adapter) buildArgs(command protocol.Command, resolved localstate.CommandContext, resume bool) ([]string, string, error) {
	cwd := firstNonEmpty(resolved.Project.Path, resolved.Session.CWD)
	args := []string{"exec"}
	if resume {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check")
	if a.cfg.Model != "" {
		args = append(args, "-m", a.cfg.Model)
	}
	if a.cfg.Profile != "" {
		args = append(args, "-p", a.cfg.Profile)
	}
	if cwd != "" {
		args = append(args, "-C", cwd)
	}
	if resume {
		nativeSessionID := firstNonEmpty(resolved.RuntimeSession.NativeSessionID, resolved.RuntimeSession.SessionID, resolved.Session.ID, command.SessionID)
		if strings.TrimSpace(nativeSessionID) == "" {
			return nil, "", errors.New("session_id is required for codex resume")
		}
		args = append(args, "--all", nativeSessionID, "-")
		return args, cwd, nil
	}
	args = append(args, "-")
	return args, cwd, nil
}
