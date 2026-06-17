package app

import (
	"context"
	"time"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

type AgentRepository interface {
	AgentBySecret(context.Context, string) (store.AgentIdentity, error)
	AgentByID(context.Context, string) (store.AgentIdentity, error)
	SaveAgentRegister(context.Context, protocol.AgentRegister) error
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
	SaveSyncCursor(context.Context, string, protocol.SyncCursor) error
	HistoryMessages(context.Context, string, string) ([]protocol.HistoryMessage, error)
	UpsertRuntimeSession(context.Context, string, protocol.RuntimeSession) error
}

type EventRepository interface {
	SaveRunEvent(context.Context, string, string, protocol.RunEvent) error
	SaveHeartbeat(context.Context, string, string, protocol.Heartbeat) error
	SaveCommand(context.Context, string, protocol.Command) (protocol.Command, error)
	MarkCommandDispatched(context.Context, string, string) error
	MarkCommandFailed(context.Context, string, string, string) error
	MarkCommandExpired(context.Context, string, string, string) error
	PrepareCommandRetry(context.Context, string, string, time.Time, time.Time) error
	MarkCommandAck(context.Context, string, protocol.Ack) error
}

type DashboardRepository interface {
	ListAgents(context.Context, string, bool) ([]store.AgentSummary, error)
	ListCommands(context.Context, string, string, int) ([]store.CommandRecord, error)
	CommandByID(context.Context, string, string) (store.CommandRecord, bool, error)
	PendingCommands(context.Context, string, int) ([]store.CommandRecord, error)
	ListProjects(context.Context, string) ([]store.ProjectSummary, error)
	ListSessions(context.Context, string, string, int) ([]store.SessionSummary, error)
	ListRuns(context.Context, string, string, int) ([]store.RunSummary, error)
	ListRunEvents(context.Context, string, string, uint64, int) ([]store.RunEventRecord, error)
	ListHistoryItems(context.Context, string, string, int, int) ([]store.HistoryItem, error)
	ListRunMessages(context.Context, string, string) ([]store.RunMessage, error)
	ListRunSteps(context.Context, string, string) ([]store.RunStep, error)
	ListToolCalls(context.Context, string, string) ([]store.ToolCall, error)
	ListRuntimeSessions(context.Context, string, string) ([]protocol.RuntimeSession, error)
	SaveApprovalRequest(context.Context, string, protocol.ApprovalRequest) error
	ListApprovals(context.Context, string, bool, string, int) ([]store.ApprovalSummary, error)
	ApprovalAgent(context.Context, string) (store.AgentIdentity, error)
	DecideApproval(context.Context, string, protocol.ApprovalDecision) error
}

type Repository interface {
	AgentRepository
	BindRepository
	IndexRepository
	EventRepository
	DashboardRepository
}
