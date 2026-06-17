package runtime

import (
	"context"

	"ov-computeruse/agent/internal/protocol"
)

type Sink interface {
	Emit(context.Context, protocol.RunEvent) error
}

type ApprovalWaiter interface {
	AwaitApproval(context.Context, protocol.ApprovalRequest) (protocol.ApprovalDecision, error)
}

type Runtime interface {
	NewSession(context.Context, protocol.Command, Sink) error
	Resume(context.Context, protocol.Command, Sink) error
	Send(context.Context, protocol.Command, Sink) error
	Stop(context.Context, protocol.Command) error
}

type Noop struct{}

func NewNoop() Noop {
	return Noop{}
}

func (Noop) NewSession(ctx context.Context, command protocol.Command, sink Sink) error {
	return emitStatus(ctx, command, sink, "runtime.noop.new_session")
}

func (Noop) Resume(ctx context.Context, command protocol.Command, sink Sink) error {
	return emitStatus(ctx, command, sink, "runtime.noop.resume")
}

func (Noop) Send(ctx context.Context, command protocol.Command, sink Sink) error {
	return emitStatus(ctx, command, sink, "runtime.noop.send")
}

func (Noop) Stop(ctx context.Context, command protocol.Command) error {
	return nil
}

func emitStatus(ctx context.Context, command protocol.Command, sink Sink, status string) error {
	if sink == nil {
		return nil
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      "run.status",
		Payload:   protocol.Raw(map[string]string{"status": status}),
	})
}
