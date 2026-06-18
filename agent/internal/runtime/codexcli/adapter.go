package codexcli

import (
	"context"
	"strings"
	"time"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

const runtimeName = protocol.RuntimeCodexCLI

type Config struct {
	BinPath string
	Model   string
	Profile string
	State   *localstate.Store
}

type Adapter struct {
	cfg    Config
	active activeRuns
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func (a *Adapter) Name() string {
	return runtimeName
}

func (a *Adapter) NewSession(ctx context.Context, command protocol.Command, sink agentruntime.Sink) error {
	return a.exec(ctx, command, sink, false)
}

func (a *Adapter) Resume(ctx context.Context, command protocol.Command, sink agentruntime.Sink) error {
	return a.exec(ctx, command, sink, true)
}

func (a *Adapter) Send(ctx context.Context, command protocol.Command, sink agentruntime.Sink) error {
	return a.exec(ctx, command, sink, true)
}

func (a *Adapter) Stop(ctx context.Context, command protocol.Command) error {
	if cancel, ok := a.active.cancel(command); ok {
		cancel()
	}
	return nil
}

func (a *Adapter) resolve(ctx context.Context, command protocol.Command) (localstate.CommandContext, error) {
	if a.cfg.State == nil {
		return localstate.CommandContext{}, nil
	}
	if strings.TrimSpace(command.ProjectID) == "" && strings.TrimSpace(command.SessionID) == "" {
		return localstate.CommandContext{}, nil
	}
	return a.cfg.State.ResolveCommandContext(ctx, command)
}

func (a *Adapter) emitRuntimeSession(ctx context.Context, command protocol.Command, resolved localstate.CommandContext, threadID string, sink agentruntime.Sink) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil
	}
	sessionID := firstNonEmpty(command.SessionID, resolved.Session.ID, threadID)
	projectID := firstNonEmpty(command.ProjectID, resolved.Project.ID, resolved.Session.ProjectID)
	runtimeSession := protocol.RuntimeSession{
		Runtime:         runtimeName,
		ProjectID:       projectID,
		SessionID:       sessionID,
		NativeSessionID: threadID,
		ResumeMode:      "codex_cli_exec",
		LastRunID:       command.RunID,
		UpdatedAt:       time.Now().UTC(),
	}
	if a.cfg.State != nil {
		_ = a.cfg.State.SaveRuntimeSession(ctx, localstate.RuntimeSession{
			SessionID:       runtimeSession.SessionID,
			Runtime:         runtimeSession.Runtime,
			ProjectID:       runtimeSession.ProjectID,
			NativeSessionID: runtimeSession.NativeSessionID,
			ResumeMode:      runtimeSession.ResumeMode,
			LastRunID:       runtimeSession.LastRunID,
			UpdatedAt:       runtimeSession.UpdatedAt,
		})
	}
	if sink == nil {
		return nil
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: runtimeSession.ProjectID,
		SessionID: runtimeSession.SessionID,
		Kind:      "session.updated",
		Payload:   protocol.Raw(runtimeSession),
	})
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
