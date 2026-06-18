package app

import (
	"context"
	"encoding/json"
	"time"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

type AgentRepository interface {
	AgentBySecret(context.Context, string) (store.AgentIdentity, error)
	AgentByID(context.Context, string) (store.AgentIdentity, error)
	SaveAgentRegister(context.Context, protocol.AgentRegister) error
	TouchAgent(context.Context, string) error
	AgentEpochMatches(context.Context, string, int64) (bool, error)
	AgentCredentialValid(context.Context, store.AgentIdentity) error
	SetAgentAccess(context.Context, string, store.AccessChange) (store.AgentIdentity, error)
	SetDeviceAccess(context.Context, string, store.AccessChange) error
	SaveAuditLog(context.Context, string, string, string, any) error
}

type BindRepository interface {
	AuthenticateAndBind(context.Context, string, string, store.DeviceProfile, store.Credential, string, string) (store.AgentIdentity, error)
	AuthenticateUser(context.Context, string, string) (store.UserIdentity, error)
	EnsureBindUser(context.Context, store.BindUser) error
}

type UserAdminRepository interface {
	ListUsers(context.Context, bool) ([]store.UserRecord, error)
	UserByID(context.Context, string) (store.UserRecord, bool, error)
	UpsertUser(context.Context, store.UserUpsert) (store.UserRecord, error)
	SetUserAccess(context.Context, string, store.AccessChange) (store.UserRecord, error)
	ListUserKeys(context.Context, string, bool) ([]store.UserKeyRecord, error)
	UserKeyByID(context.Context, string) (store.UserKeyRecord, bool, error)
	UpsertUserKey(context.Context, store.UserKeyUpsert) (store.UserKeyRecord, error)
	SetUserKeyAccess(context.Context, string, store.AccessChange) (store.UserKeyRecord, error)
	ListAuditLogs(context.Context, store.AuditLogFilter) ([]store.AuditLogRecord, error)
}

type IndexRepository interface {
	SaveRoots(context.Context, string, []protocol.Root) error
	SaveProjects(context.Context, string, []protocol.Project) error
	SaveSessions(context.Context, string, []protocol.Session) error
	ProjectExists(context.Context, string, string) (bool, error)
	SessionExists(context.Context, string, string) (bool, error)
	MarkIndexDeleted(context.Context, string, protocol.DeletedIndex) error
	SaveHistoryChunk(context.Context, string, protocol.HistoryChunk) error
	SaveHistoryMessages(context.Context, string, protocol.HistoryMessages) error
	SaveHistoryItems(context.Context, string, protocol.HistoryItems) error
	SaveSyncCursor(context.Context, string, protocol.SyncCursor) error
	HistoryMessages(context.Context, string, string) ([]protocol.HistoryMessage, error)
	UpsertRuntimeSession(context.Context, string, protocol.RuntimeSession) error
}

type EventRepository interface {
	SaveRunEvent(context.Context, string, string, protocol.RunEvent) error
	RebuildRunProjections(context.Context, string, string) (store.ProjectionRebuildResult, error)
	SaveHeartbeat(context.Context, string, string, protocol.Heartbeat) error
	SaveCommand(context.Context, string, protocol.Command) (protocol.Command, error)
	SaveCommandAttempt(context.Context, string, string, string, string, string, json.RawMessage) error
	MarkCommandDispatched(context.Context, string, string) error
	MarkCommandFailed(context.Context, string, string, string) error
	MarkCommandExpired(context.Context, string, string, string) error
	ReleaseApprovalDecisionCommand(context.Context, string, string, string) error
	PrepareCommandRetry(context.Context, string, string, time.Time, time.Time) error
	ExpireCommands(context.Context, string) error
	MarkCommandAck(context.Context, string, protocol.Ack) error
}

type DashboardRepository interface {
	ListAgents(context.Context, string, bool) ([]store.AgentSummary, error)
	ListCommands(context.Context, string, string, int) ([]store.CommandRecord, error)
	CommandByID(context.Context, string, string) (store.CommandRecord, bool, error)
	ListCommandAttempts(context.Context, string, string, int) ([]store.CommandAttempt, error)
	PendingCommands(context.Context, string, int) ([]store.CommandRecord, error)
	ClaimCommand(context.Context, string, string, string) (store.CommandRecord, bool, error)
	ClaimPendingCommands(context.Context, string, string, int) ([]store.CommandRecord, error)
	ClaimDispatchCommands(context.Context, string, int) ([]store.CommandRecord, error)
	ListProjects(context.Context, string) ([]store.ProjectSummary, error)
	ListSessions(context.Context, string, string, int) ([]store.SessionSummary, error)
	ListRuns(context.Context, string, string, int) ([]store.RunSummary, error)
	RunExists(context.Context, string, string) (bool, error)
	ListRunEvents(context.Context, string, string, uint64, int) ([]store.RunEventRecord, error)
	ListHistoryItems(context.Context, string, string, int, int) ([]store.HistoryItem, error)
	ListRunMessages(context.Context, string, string) ([]store.RunMessage, error)
	ListRunSteps(context.Context, string, string) ([]store.RunStep, error)
	ListToolCalls(context.Context, string, string) ([]store.ToolCall, error)
	ListRuntimeSessions(context.Context, string, string) ([]protocol.RuntimeSession, error)
	SaveApprovalRequest(context.Context, string, protocol.ApprovalRequest) error
	ListApprovals(context.Context, string, bool, string, int) ([]store.ApprovalSummary, error)
	ApprovalByID(context.Context, string) (store.ApprovalSummary, bool, error)
	ApprovalAgent(context.Context, string) (store.AgentIdentity, error)
	QueueApprovalDecisionCommand(context.Context, string, string, protocol.ApprovalDecision, protocol.Command) (protocol.Command, error)
	DecideApproval(context.Context, string, protocol.ApprovalDecision) error
}

type Repository interface {
	AgentRepository
	BindRepository
	UserAdminRepository
	IndexRepository
	EventRepository
	DashboardRepository
}
