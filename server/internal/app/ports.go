package app

import (
	"context"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

type AgentRepository interface {
	AgentBySecret(context.Context, string) (store.AgentIdentity, error)
	AgentByID(context.Context, string) (store.AgentIdentity, error)
	TouchAgent(context.Context, string) error
}

type BindRepository interface {
	AuthenticateAndBind(context.Context, string, string, store.DeviceProfile, store.Credential, string, string) (store.AgentIdentity, error)
	AuthenticateUser(context.Context, string, string) (store.UserIdentity, error)
	EnsureBindUser(context.Context, store.BindUser) error
}

type IndexRepository interface {
	SaveRoots(context.Context, string, []protocol.Root) error
	SaveProjects(context.Context, string, []protocol.Project) error
	SaveSessions(context.Context, string, []protocol.Session) error
	SaveHistoryChunk(context.Context, string, protocol.HistoryChunk) error
	SaveHistoryMessages(context.Context, string, protocol.HistoryMessages) error
	HistoryMessages(context.Context, string, string) ([]protocol.HistoryMessage, error)
}

type EventRepository interface {
	SaveRunEvent(context.Context, string, string, protocol.RunEvent) error
	SaveHeartbeat(context.Context, string, string, protocol.Heartbeat) error
	SaveCommand(context.Context, string, protocol.Command) error
	MarkCommandDispatched(context.Context, string, string) error
	MarkCommandFailed(context.Context, string, string) error
	MarkCommandAck(context.Context, string, protocol.Ack) error
}

type Repository interface {
	AgentRepository
	BindRepository
	IndexRepository
	EventRepository
}
