package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
)

type AgentSummary struct {
	ID           string          `json:"id"`
	WorkspaceID  string          `json:"workspace_id"`
	UserID       string          `json:"user_id,omitempty"`
	DeviceID     string          `json:"device_id"`
	Hostname     string          `json:"hostname,omitempty"`
	OS           string          `json:"os,omitempty"`
	Arch         string          `json:"arch,omitempty"`
	Version      string          `json:"version,omitempty"`
	Status       string          `json:"status,omitempty"`
	LastSeenAt   time.Time       `json:"last_seen_at,omitempty"`
	Heartbeat    json.RawMessage `json:"heartbeat,omitempty"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	Credential   json.RawMessage `json:"credential,omitempty"`
	InstallState json.RawMessage `json:"install_state,omitempty"`
	RegisteredAt time.Time       `json:"registered_at,omitempty"`
	Health       json.RawMessage `json:"health,omitempty"`
}

type ProjectSummary struct {
	ID           string    `json:"id"`
	AgentID      string    `json:"agent_id"`
	Name         string    `json:"name,omitempty"`
	Path         string    `json:"path,omitempty"`
	LastActiveAt time.Time `json:"last_active_at,omitempty"`
	HasAgentsMD  bool      `json:"has_agents_md"`
	GitBranch    string    `json:"git_branch,omitempty"`
	UpdatedAt    time.Time `json:"updated_at,omitempty"`
	SessionCount int       `json:"session_count"`
}

type SessionSummary struct {
	ID            string    `json:"id"`
	IDSource      string    `json:"id_source,omitempty"`
	AgentID       string    `json:"agent_id"`
	ProjectID     string    `json:"project_id,omitempty"`
	Title         string    `json:"title,omitempty"`
	Path          string    `json:"path,omitempty"`
	CWD           string    `json:"cwd,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	Size          int64     `json:"size,omitempty"`
	ContentSHA256 string    `json:"content_sha256,omitempty"`
	MessageCount  int       `json:"message_count"`
	LastMessageAt time.Time `json:"last_message_at,omitempty"`
}

type RunSummary struct {
	ID            string    `json:"id"`
	AgentID       string    `json:"agent_id"`
	CommandID     string    `json:"command_id,omitempty"`
	ProjectID     string    `json:"project_id,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	Status        string    `json:"status"`
	StatusReason  string    `json:"status_reason,omitempty"`
	LastEventSeq  uint64    `json:"last_event_seq"`
	LastEventAt   time.Time `json:"last_event_at,omitempty"`
	StartedAt     time.Time `json:"started_at"`
	FinishedAt    time.Time `json:"finished_at,omitempty"`
	EventGapCount int       `json:"event_gap_count"`
}

type CommandRecord struct {
	ID             string          `json:"id"`
	AgentID        string          `json:"agent_id"`
	RunID          string          `json:"run_id,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	ProjectID      string          `json:"project_id,omitempty"`
	Kind           string          `json:"kind"`
	Mode           string          `json:"mode,omitempty"`
	Payload        json.RawMessage `json:"payload,omitempty"`
	Status         string          `json:"status"`
	StatusReason   string          `json:"status_reason,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	DispatchedAt   time.Time       `json:"dispatched_at,omitempty"`
	AckedAt        time.Time       `json:"acked_at,omitempty"`
	DeadlineAt     time.Time       `json:"deadline_at,omitempty"`
	ExpiresAt      time.Time       `json:"expires_at,omitempty"`
	RetryCount     int             `json:"retry_count"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

func (c CommandRecord) ToProtocol() protocol.Command {
	return protocol.Command{
		CommandID:      c.ID,
		RunID:          c.RunID,
		Kind:           c.Kind,
		ProjectID:      c.ProjectID,
		SessionID:      c.SessionID,
		Mode:           c.Mode,
		IdempotencyKey: c.IdempotencyKey,
		DeadlineAt:     c.DeadlineAt,
		ExpiresAt:      c.ExpiresAt,
		Payload:        c.Payload,
	}
}

type RunEventRecord struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	DeviceID   string          `json:"device_id"`
	RunID      string          `json:"run_id,omitempty"`
	CommandID  string          `json:"command_id,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Seq        uint64          `json:"seq"`
	Kind       string          `json:"kind"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	EventAt    time.Time       `json:"event_at"`
	ReceivedAt time.Time       `json:"received_at"`
}

type ApprovalSummary struct {
	ID          string          `json:"id"`
	AgentID     string          `json:"agent_id"`
	RunID       string          `json:"run_id,omitempty"`
	ProjectID   string          `json:"project_id,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	Category    string          `json:"category,omitempty"`
	Action      string          `json:"action,omitempty"`
	RiskLevel   string          `json:"risk_level,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Status      string          `json:"status"`
	RequestedAt time.Time       `json:"requested_at"`
	DecidedAt   time.Time       `json:"decided_at,omitempty"`
}

func (s *Store) ListAgents(ctx context.Context, userID string, admin bool) ([]AgentSummary, error) {
	query := `SELECT a.id, a.workspace_id, a.user_id, a.device_id, COALESCE(d.hostname, ''), COALESCE(d.os, ''), COALESCE(d.arch, ''), COALESCE(d.agent_version, ''), COALESCE(h.status, ''), COALESCE(a.last_seen_at, d.last_seen_at), h.payload, a.capabilities, a.credential, d.install_state, a.registered_at
		FROM agents a
		JOIN devices d ON d.id = a.device_id
		LEFT JOIN heartbeats h ON h.agent_id = a.id`
	args := []any{}
	if !admin {
		query += ` WHERE a.user_id=$1`
		args = append(args, userID)
	}
	query += ` ORDER BY COALESCE(a.last_seen_at, d.last_seen_at, a.created_at) DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AgentSummary{}
	for rows.Next() {
		var item AgentSummary
		var lastSeen sql.NullTime
		var registeredAt sql.NullTime
		var heartbeat []byte
		var capabilities []byte
		var credential []byte
		var installState []byte
		if err := rows.Scan(&item.ID, &item.WorkspaceID, &item.UserID, &item.DeviceID, &item.Hostname, &item.OS, &item.Arch, &item.Version, &item.Status, &lastSeen, &heartbeat, &capabilities, &credential, &installState, &registeredAt); err != nil {
			return nil, err
		}
		if lastSeen.Valid {
			item.LastSeenAt = lastSeen.Time
		}
		if registeredAt.Valid {
			item.RegisteredAt = registeredAt.Time
		}
		if len(heartbeat) > 0 {
			item.Heartbeat = append(json.RawMessage(nil), heartbeat...)
			var hb protocol.Heartbeat
			if json.Unmarshal(heartbeat, &hb) == nil && hb.Health.Status != "" {
				item.Health = protocol.Raw(hb.Health)
			}
		}
		if len(capabilities) > 0 {
			item.Capabilities = append(json.RawMessage(nil), capabilities...)
		}
		if len(credential) > 0 {
			item.Credential = append(json.RawMessage(nil), credential...)
		}
		if len(installState) > 0 {
			item.InstallState = append(json.RawMessage(nil), installState...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListProjects(ctx context.Context, agentID string) ([]ProjectSummary, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.agent_id, p.id, COALESCE(p.name, ''), COALESCE(p.path, ''), p.last_active_at, p.has_agents_md, COALESCE(p.git_branch, ''), p.updated_at, COUNT(cs.id)
		FROM projects p
		LEFT JOIN codex_sessions cs ON cs.agent_id = p.agent_id AND cs.project_id = p.id
		WHERE p.agent_id=$1 AND p.deleted_at IS NULL
		GROUP BY p.agent_id, p.id, p.name, p.path, p.last_active_at, p.has_agents_md, p.git_branch, p.updated_at
		ORDER BY COALESCE(p.last_active_at, p.updated_at) DESC`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectSummary{}
	for rows.Next() {
		var item ProjectSummary
		var lastActive sql.NullTime
		if err := rows.Scan(&item.AgentID, &item.ID, &item.Name, &item.Path, &lastActive, &item.HasAgentsMD, &item.GitBranch, &item.UpdatedAt, &item.SessionCount); err != nil {
			return nil, err
		}
		if lastActive.Valid {
			item.LastActiveAt = lastActive.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListSessions(ctx context.Context, agentID, projectID string, limit int) ([]SessionSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `SELECT cs.agent_id, cs.id, COALESCE(cs.id_source, ''), COALESCE(cs.project_id, ''), COALESCE(cs.title, ''), COALESCE(cs.path, ''), COALESCE(cs.cwd, ''), cs.updated_at, COALESCE(cs.size_bytes, 0), COALESCE(cs.content_sha256, ''), COUNT(hi.item_index), MAX(hi.item_at)
		FROM codex_sessions cs
		LEFT JOIN history_items hi ON hi.agent_id = cs.agent_id AND hi.session_id = cs.id
		WHERE cs.agent_id=$1 AND cs.deleted_at IS NULL`
	args := []any{agentID}
	if projectID != "" {
		query += ` AND cs.project_id=$2`
		args = append(args, projectID)
	}
	query += ` GROUP BY cs.agent_id, cs.id, cs.id_source, cs.project_id, cs.title, cs.path, cs.cwd, cs.updated_at, cs.size_bytes, cs.content_sha256
		ORDER BY COALESCE(cs.updated_at, MAX(hi.item_at), now()) DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SessionSummary{}
	for rows.Next() {
		var item SessionSummary
		var updatedAt sql.NullTime
		var lastMessageAt sql.NullTime
		if err := rows.Scan(&item.AgentID, &item.ID, &item.IDSource, &item.ProjectID, &item.Title, &item.Path, &item.CWD, &updatedAt, &item.Size, &item.ContentSHA256, &item.MessageCount, &lastMessageAt); err != nil {
			return nil, err
		}
		if updatedAt.Valid {
			item.UpdatedAt = updatedAt.Time
		}
		if lastMessageAt.Valid {
			item.LastMessageAt = lastMessageAt.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListRuns(ctx context.Context, agentID, sessionID string, limit int) ([]RunSummary, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := `SELECT r.id, r.agent_id, COALESCE(r.command_id, ''), COALESCE(r.project_id, ''), COALESCE(r.session_id, ''), r.status, COALESCE(r.status_reason, ''), r.last_event_seq, r.last_event_at, r.started_at, r.finished_at, COUNT(reg.id)
		FROM runs r
		LEFT JOIN run_event_gaps reg ON reg.agent_id = r.agent_id AND reg.run_id = r.id AND reg.status = 'open'
		WHERE r.agent_id=$1`
	args := []any{agentID}
	if sessionID != "" {
		query += ` AND r.session_id=$2`
		args = append(args, sessionID)
	}
	query += ` GROUP BY r.id, r.agent_id, r.command_id, r.project_id, r.session_id, r.status, r.status_reason, r.last_event_seq, r.last_event_at, r.started_at, r.finished_at
		ORDER BY r.started_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunSummary{}
	for rows.Next() {
		var item RunSummary
		var lastEventAt sql.NullTime
		var finished sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.CommandID, &item.ProjectID, &item.SessionID, &item.Status, &item.StatusReason, &item.LastEventSeq, &lastEventAt, &item.StartedAt, &finished, &item.EventGapCount); err != nil {
			return nil, err
		}
		if lastEventAt.Valid {
			item.LastEventAt = lastEventAt.Time
		}
		if finished.Valid {
			item.FinishedAt = finished.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListCommands(ctx context.Context, agentID, status string, limit int) ([]CommandRecord, error) {
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	query := `SELECT id, agent_id, COALESCE(run_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), kind, COALESCE(mode, ''), payload, status, COALESCE(status_reason, ''), created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, COALESCE(idempotency_key, '')
		FROM commands WHERE agent_id=$1`
	args := []any{agentID}
	if status != "" {
		args = append(args, status)
		query += ` AND status=$` + strconv.Itoa(len(args))
	}
	args = append(args, limit)
	query += ` ORDER BY created_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandRecord{}
	for rows.Next() {
		item, err := scanCommandRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) CommandByID(ctx context.Context, agentID, commandID string) (CommandRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT id, agent_id, COALESCE(run_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), kind, COALESCE(mode, ''), payload, status, COALESCE(status_reason, ''), created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, COALESCE(idempotency_key, '')
		FROM commands WHERE agent_id=$1 AND id=$2`, agentID, commandID)
	item, err := scanCommandRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CommandRecord{}, false, nil
		}
		return CommandRecord{}, false, err
	}
	return item, true, nil
}

func (s *Store) CommandByIdempotencyKey(ctx context.Context, agentID, key string) (CommandRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT id, agent_id, COALESCE(run_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), kind, COALESCE(mode, ''), payload, status, COALESCE(status_reason, ''), created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, COALESCE(idempotency_key, '')
		FROM commands WHERE agent_id=$1 AND idempotency_key=$2`, agentID, key)
	item, err := scanCommandRecord(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CommandRecord{}, false, nil
		}
		return CommandRecord{}, false, err
	}
	return item, true, nil
}

func (s *Store) PendingCommands(ctx context.Context, agentID string, limit int) ([]CommandRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, COALESCE(run_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), kind, COALESCE(mode, ''), payload, status, COALESCE(status_reason, ''), created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, COALESCE(idempotency_key, '')
		FROM commands
		WHERE agent_id=$1
			AND status IN ('queued','dispatch_failed','dispatched')
			AND (expires_at IS NULL OR expires_at > now())
		ORDER BY created_at ASC
		LIMIT $2`, agentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandRecord{}
	for rows.Next() {
		item, err := scanCommandRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) PendingDispatchCommands(ctx context.Context, limit int) ([]CommandRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, COALESCE(run_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), kind, COALESCE(mode, ''), payload, status, COALESCE(status_reason, ''), created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, COALESCE(idempotency_key, '')
		FROM commands
		WHERE status IN ('queued','dispatch_failed','dispatched')
			AND (expires_at IS NULL OR expires_at > now())
			AND (dispatched_at IS NULL OR dispatched_at < now() - interval '15 seconds')
		ORDER BY created_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommandRecord{}
	for rows.Next() {
		item, err := scanCommandRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type commandRowScanner interface {
	Scan(dest ...any) error
}

func scanCommandRecord(scanner commandRowScanner) (CommandRecord, error) {
	var item CommandRecord
	var payload []byte
	var dispatchedAt sql.NullTime
	var ackedAt sql.NullTime
	var deadlineAt sql.NullTime
	var expiresAt sql.NullTime
	if err := scanner.Scan(&item.ID, &item.AgentID, &item.RunID, &item.SessionID, &item.ProjectID, &item.Kind, &item.Mode, &payload, &item.Status, &item.StatusReason, &item.CreatedAt, &dispatchedAt, &ackedAt, &deadlineAt, &expiresAt, &item.RetryCount, &item.IdempotencyKey); err != nil {
		return CommandRecord{}, err
	}
	if len(payload) > 0 {
		item.Payload = append(json.RawMessage(nil), payload...)
	}
	if dispatchedAt.Valid {
		item.DispatchedAt = dispatchedAt.Time
	}
	if ackedAt.Valid {
		item.AckedAt = ackedAt.Time
	}
	if deadlineAt.Valid {
		item.DeadlineAt = deadlineAt.Time
	}
	if expiresAt.Valid {
		item.ExpiresAt = expiresAt.Time
	}
	return item, nil
}

func (s *Store) ListRunEvents(ctx context.Context, agentID, runID string, afterSeq uint64, limit int) ([]RunEventRecord, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, device_id, COALESCE(run_id, ''), COALESCE(command_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), seq, kind, payload, event_at, received_at
		FROM run_events
		WHERE agent_id=$1 AND run_id=$2 AND seq>$3
		ORDER BY seq ASC, received_at ASC
		LIMIT $4`, agentID, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunEventRecord{}
	for rows.Next() {
		var item RunEventRecord
		var payload []byte
		if err := rows.Scan(&item.ID, &item.AgentID, &item.DeviceID, &item.RunID, &item.CommandID, &item.SessionID, &item.ProjectID, &item.Seq, &item.Kind, &payload, &item.EventAt, &item.ReceivedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) UpsertRuntimeSession(ctx context.Context, agentID string, runtime protocol.RuntimeSession) error {
	if runtime.Runtime == "" {
		runtime.Runtime = "codex"
	}
	if runtime.ID == "" {
		runtime.ID = runtimeSessionID(agentID, runtime.Runtime, dashboardFirstNonEmpty(runtime.NativeSessionID, runtime.SessionID, runtime.LastResponseID, runtime.LastRunID))
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO runtime_sessions (id, agent_id, runtime, native_session_id, project_id, session_id, last_response_id, resume_mode, last_run_id, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())
		ON CONFLICT (id) DO UPDATE SET
			runtime=EXCLUDED.runtime,
			native_session_id=COALESCE(NULLIF(EXCLUDED.native_session_id, ''), runtime_sessions.native_session_id),
			project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runtime_sessions.project_id),
			session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runtime_sessions.session_id),
			last_response_id=COALESCE(NULLIF(EXCLUDED.last_response_id, ''), runtime_sessions.last_response_id),
			resume_mode=COALESCE(NULLIF(EXCLUDED.resume_mode, ''), runtime_sessions.resume_mode),
			last_run_id=COALESCE(NULLIF(EXCLUDED.last_run_id, ''), runtime_sessions.last_run_id),
			updated_at=now()`,
		runtime.ID, agentID, runtime.Runtime, runtime.NativeSessionID, runtime.ProjectID, runtime.SessionID, runtime.LastResponseID, runtime.ResumeMode, runtime.LastRunID)
	return err
}

func (s *Store) ListRuntimeSessions(ctx context.Context, agentID, sessionID string) ([]protocol.RuntimeSession, error) {
	query := `SELECT id, runtime, COALESCE(project_id, ''), COALESCE(session_id, ''), COALESCE(native_session_id, ''), COALESCE(last_response_id, ''), COALESCE(resume_mode, ''), COALESCE(last_run_id, ''), updated_at FROM runtime_sessions WHERE agent_id=$1`
	args := []any{agentID}
	if sessionID != "" {
		query += ` AND session_id=$2`
		args = append(args, sessionID)
	}
	query += ` ORDER BY updated_at DESC`
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []protocol.RuntimeSession{}
	for rows.Next() {
		var item protocol.RuntimeSession
		if err := rows.Scan(&item.ID, &item.Runtime, &item.ProjectID, &item.SessionID, &item.NativeSessionID, &item.LastResponseID, &item.ResumeMode, &item.LastRunID, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) SaveApprovalRequest(ctx context.Context, agentID string, request protocol.ApprovalRequest) error {
	if request.ID == "" {
		request.ID = protocol.NewID("apr")
	}
	if request.At.IsZero() {
		request.At = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO approval_requests (id, agent_id, run_id, project_id, session_id, category, action, risk_level, payload, status, requested_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'pending',$10)
		ON CONFLICT (id) DO NOTHING`, request.ID, agentID, request.RunID, request.ProjectID, request.SessionID, request.Category, request.Action, request.RiskLevel, jsonRaw(request.Payload), request.At)
	return err
}

func (s *Store) ListApprovals(ctx context.Context, userID string, admin bool, status string, limit int) ([]ApprovalSummary, error) {
	if limit <= 0 || limit > 300 {
		limit = 100
	}
	query := `SELECT ar.id, ar.agent_id, COALESCE(ar.run_id, ''), COALESCE(ar.project_id, ''), COALESCE(ar.session_id, ''), COALESCE(ar.category, ''), COALESCE(ar.action, ''), COALESCE(ar.risk_level, ''), ar.payload, ar.status, ar.requested_at, ar.decided_at
		FROM approval_requests ar
		JOIN agents a ON a.id = ar.agent_id`
	args := []any{}
	where := []string{}
	if !admin {
		args = append(args, userID)
		where = append(where, "a.user_id=$"+strconv.Itoa(len(args)))
	}
	if status != "" {
		args = append(args, status)
		where = append(where, "ar.status=$"+strconv.Itoa(len(args)))
	}
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	query += ` ORDER BY ar.requested_at DESC LIMIT $` + strconv.Itoa(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ApprovalSummary{}
	for rows.Next() {
		var item ApprovalSummary
		var payload []byte
		var decidedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.RunID, &item.ProjectID, &item.SessionID, &item.Category, &item.Action, &item.RiskLevel, &payload, &item.Status, &item.RequestedAt, &decidedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		if decidedAt.Valid {
			item.DecidedAt = decidedAt.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ApprovalAgent(ctx context.Context, approvalID string) (AgentIdentity, error) {
	var identity AgentIdentity
	err := s.pool.QueryRow(ctx, `SELECT a.id, a.workspace_id, a.user_id, a.device_id, a.agent_secret, a.server_key_id
		FROM approval_requests ar
		JOIN agents a ON a.id = ar.agent_id
		WHERE ar.id=$1`, approvalID).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID)
	return identity, err
}

func (s *Store) DecideApproval(ctx context.Context, approvalID string, decision protocol.ApprovalDecision) error {
	_, err := s.pool.Exec(ctx, `UPDATE approval_requests SET status=$1, decided_at=$2 WHERE id=$3 AND status='pending'`, decision.Decision, decision.DecidedAt, approvalID)
	return err
}

func runtimeSessionID(agentID, runtime, native string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + runtime + "\x00" + native))
	return "rts_" + hex.EncodeToString(sum[:])[:32]
}

func dashboardFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return "unknown"
}
