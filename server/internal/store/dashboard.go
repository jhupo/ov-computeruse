package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

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
	ID         string    `json:"id"`
	AgentID    string    `json:"agent_id"`
	ProjectID  string    `json:"project_id,omitempty"`
	SessionID  string    `json:"session_id,omitempty"`
	Status     string    `json:"status"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
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
		WHERE p.agent_id=$1
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
	query := `SELECT cs.agent_id, cs.id, COALESCE(cs.id_source, ''), COALESCE(cs.project_id, ''), COALESCE(cs.title, ''), COALESCE(cs.path, ''), COALESCE(cs.cwd, ''), cs.updated_at, COALESCE(cs.size_bytes, 0), COALESCE(cs.content_sha256, ''), COUNT(hm.message_index), MAX(hm.message_at)
		FROM codex_sessions cs
		LEFT JOIN history_messages hm ON hm.agent_id = cs.agent_id AND hm.session_id = cs.id
		WHERE cs.agent_id=$1`
	args := []any{agentID}
	if projectID != "" {
		query += ` AND cs.project_id=$2`
		args = append(args, projectID)
	}
	query += ` GROUP BY cs.agent_id, cs.id, cs.id_source, cs.project_id, cs.title, cs.path, cs.cwd, cs.updated_at, cs.size_bytes, cs.content_sha256
		ORDER BY COALESCE(cs.updated_at, MAX(hm.message_at), now()) DESC LIMIT $` + strconv.Itoa(len(args)+1)
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
	query := `SELECT id, agent_id, COALESCE(project_id, ''), COALESCE(session_id, ''), status, started_at, finished_at FROM runs WHERE agent_id=$1`
	args := []any{agentID}
	if sessionID != "" {
		query += ` AND session_id=$2`
		args = append(args, sessionID)
	}
	query += ` ORDER BY started_at DESC LIMIT $` + strconv.Itoa(len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunSummary{}
	for rows.Next() {
		var item RunSummary
		var finished sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.ProjectID, &item.SessionID, &item.Status, &item.StartedAt, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			item.FinishedAt = finished.Time
		}
		out = append(out, item)
	}
	return out, rows.Err()
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
