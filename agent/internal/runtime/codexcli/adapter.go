package codexcli

import (
	"context"
	"errors"
	"strings"
	"time"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

const runtimeName = protocol.RuntimeCodexCLI

type Config struct {
	BinPath        string
	Model          string
	Profile        string
	State          *localstate.Store
	IndexRefresher IndexRefresher
}

type IndexRefresher interface {
	RefreshCodexIndex(context.Context) error
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
	resolved, err := a.cfg.State.ResolveCommandContext(ctx, command)
	if err == nil || a.cfg.IndexRefresher == nil || !isMissingLocalIndex(err) {
		return resolved, err
	}
	if refreshErr := a.cfg.IndexRefresher.RefreshCodexIndex(ctx); refreshErr != nil {
		return resolved, errors.Join(err, refreshErr)
	}
	return a.cfg.State.ResolveCommandContext(ctx, command)
}

func isMissingLocalIndex(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not indexed locally")
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
		Title:           firstNonEmpty(resolved.Session.Title, threadID),
		CWD:             firstNonEmpty(resolved.Session.CWD, resolved.Project.Path),
		Model:           a.cfg.Model,
		Profile:         a.cfg.Profile,
		ApprovalPolicy:  "never",
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
			Title:           runtimeSession.Title,
			CWD:             runtimeSession.CWD,
			Model:           runtimeSession.Model,
			Profile:         runtimeSession.Profile,
			ApprovalPolicy:  runtimeSession.ApprovalPolicy,
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
