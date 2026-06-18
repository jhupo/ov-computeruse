package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
)

type ProjectionRebuildResult struct {
	AgentID      string    `json:"agent_id"`
	RunID        string    `json:"run_id"`
	EventCount   int       `json:"event_count"`
	LastEventSeq uint64    `json:"last_event_seq"`
	LastEventAt  time.Time `json:"last_event_at,omitempty"`
	RebuiltAt    time.Time `json:"rebuilt_at"`
}

func (s *Store) SaveRunEvent(ctx context.Context, agentID, deviceID string, event protocol.RunEvent) error {
	if skipRunEvent(event) {
		return nil
	}
	if event.EventID == "" {
		event.EventID = protocol.NewID("evt")
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if err := s.validateRunEventOwnership(ctx, agentID, event); err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO run_events (id, agent_id, device_id, run_id, command_id, session_id, project_id, seq, kind, payload, event_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT DO NOTHING`, event.EventID, agentID, deviceID, event.RunID, event.CommandID, event.SessionID, event.ProjectID, event.Seq, event.Kind, jsonRaw(event.Payload), event.At)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if err := s.recordRunEventConsistency(ctx, agentID, event); err != nil {
		return err
	}
	if err := s.advanceRunEventCursor(ctx, agentID, event); err != nil {
		return err
	}
	if err := s.projectApproval(ctx, agentID, event); err != nil {
		return err
	}
	if err := s.projectRuntimeSession(ctx, agentID, event); err != nil {
		return err
	}
	if err := s.projectRunEvent(ctx, agentID, event); err != nil {
		return err
	}
	if err := s.projectRunState(ctx, agentID, event); err != nil {
		return err
	}
	return s.projectCommandStateFromRunEvent(ctx, agentID, event, true)
}

func (s *Store) validateRunEventOwnership(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if strings.TrimSpace(event.CommandID) != "" {
		command, found, err := s.CommandByID(ctx, agentID, event.CommandID)
		if err != nil {
			return err
		}
		if !found {
			return errors.New("run event command does not belong to agent")
		}
		if event.RunID != "" && command.RunID != "" && command.RunID != event.RunID {
			return errors.New("run event command/run mismatch")
		}
		if event.SessionID != "" && command.SessionID != "" && command.SessionID != event.SessionID {
			return errors.New("run event command/session mismatch")
		}
		if event.ProjectID != "" && command.ProjectID != "" && command.ProjectID != event.ProjectID {
			return errors.New("run event command/project mismatch")
		}
	}
	if strings.TrimSpace(event.RunID) != "" {
		exists, err := s.RunExists(ctx, agentID, event.RunID)
		if err != nil {
			return err
		}
		if !exists && strings.TrimSpace(event.CommandID) == "" {
			return nil
		}
		if !exists {
			return errors.New("run event run does not belong to agent")
		}
	}
	if strings.TrimSpace(event.SessionID) != "" {
		exists, err := s.SessionExists(ctx, agentID, event.SessionID)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("run event session does not belong to agent")
		}
	}
	if strings.TrimSpace(event.ProjectID) != "" {
		exists, err := s.ProjectExists(ctx, agentID, event.ProjectID)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("run event project does not belong to agent")
		}
	}
	return nil
}

func skipRunEvent(event protocol.RunEvent) bool {
	return protocol.IsUsageKind(event.Kind)
}

func (s *Store) RebuildRunProjections(ctx context.Context, agentID, runID string) (ProjectionRebuildResult, error) {
	result := ProjectionRebuildResult{AgentID: agentID, RunID: runID, RebuiltAt: time.Now().UTC()}
	rows, err := s.pool.Query(ctx, `SELECT id, COALESCE(command_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), seq, kind, payload, event_at
		FROM run_events
		WHERE agent_id=$1 AND run_id=$2
		ORDER BY seq ASC, received_at ASC`, agentID, runID)
	if err != nil {
		return result, err
	}
	defer rows.Close()

	events := []protocol.RunEvent{}
	for rows.Next() {
		var event protocol.RunEvent
		var payload []byte
		if err := rows.Scan(&event.EventID, &event.CommandID, &event.SessionID, &event.ProjectID, &event.Seq, &event.Kind, &payload, &event.At); err != nil {
			return result, err
		}
		event.RunID = runID
		if len(payload) > 0 {
			event.Payload = append(json.RawMessage(nil), payload...)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(events) == 0 {
		return result, sql.ErrNoRows
	}
	if err := s.clearRunProjections(ctx, agentID, runID); err != nil {
		return result, err
	}
	expectedSeq := uint64(1)
	for _, event := range events {
		if event.Seq > expectedSeq {
			if err := s.saveRunEventGap(ctx, agentID, runID, expectedSeq, event.Seq, "gap"); err != nil {
				return result, err
			}
		} else if event.Seq < expectedSeq {
			kind := "duplicate_seq"
			if event.Seq+1 < expectedSeq {
				kind = "seq_regression"
			}
			if err := s.saveRunEventGap(ctx, agentID, runID, expectedSeq, event.Seq, kind); err != nil {
				return result, err
			}
		}
		if err := s.advanceRunEventCursor(ctx, agentID, event); err != nil {
			return result, err
		}
		if err := s.projectApproval(ctx, agentID, event); err != nil {
			return result, err
		}
		if err := s.projectRuntimeSession(ctx, agentID, event); err != nil {
			return result, err
		}
		if err := s.projectRunEvent(ctx, agentID, event); err != nil {
			return result, err
		}
		if err := s.projectRunState(ctx, agentID, event); err != nil {
			return result, err
		}
		if err := s.projectCommandStateFromRunEvent(ctx, agentID, event, false); err != nil {
			return result, err
		}
		result.EventCount++
		if event.Seq > result.LastEventSeq {
			result.LastEventSeq = event.Seq
			result.LastEventAt = event.At
		}
		if event.Seq >= expectedSeq {
			expectedSeq = event.Seq + 1
		}
	}
	return result, nil
}

func (s *Store) clearRunProjections(ctx context.Context, agentID, runID string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM run_steps WHERE agent_id=$1 AND run_id=$2`, agentID, runID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM run_messages WHERE agent_id=$1 AND run_id=$2`, agentID, runID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM tool_calls WHERE agent_id=$1 AND run_id=$2`, agentID, runID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM approval_requests WHERE agent_id=$1 AND run_id=$2`, agentID, runID); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM run_event_gaps WHERE agent_id=$1 AND run_id=$2`, agentID, runID); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE runs SET status='rebuilding', status_reason='projection rebuild requested', last_event_seq=0, last_event_at=NULL, finished_at=NULL WHERE agent_id=$1 AND id=$2`, agentID, runID)
	return err
}

func (s *Store) recordRunEventConsistency(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if event.RunID == "" || event.Seq == 0 {
		return nil
	}
	if err := s.resolveRunEventGaps(ctx, agentID, event.RunID, event.Seq); err != nil {
		return err
	}
	var previous uint64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM run_events WHERE agent_id=$1 AND run_id=$2 AND seq<$3`, agentID, event.RunID, event.Seq).Scan(&previous); err != nil {
		return err
	}
	if previous > 0 && event.Seq > previous+1 {
		if err := s.saveRunEventGap(ctx, agentID, event.RunID, previous+1, event.Seq, "gap"); err != nil {
			return err
		}
	}
	var next uint64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MIN(seq), 0) FROM run_events WHERE agent_id=$1 AND run_id=$2 AND seq>$3`, agentID, event.RunID, event.Seq).Scan(&next); err != nil {
		return err
	}
	if next > 0 && next > event.Seq+1 {
		return s.saveRunEventGap(ctx, agentID, event.RunID, event.Seq+1, next, "gap")
	}
	return nil
}

func (s *Store) advanceRunEventCursor(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if event.RunID == "" || event.Seq == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, status_reason, last_event_seq, last_event_at, started_at)
		VALUES ($1,$2,$3,$4,$5,'running','event_received',$6,$7,$7)
		ON CONFLICT (agent_id, id) DO UPDATE SET
			command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id),
			project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runs.project_id),
			session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runs.session_id),
			last_event_seq=GREATEST(runs.last_event_seq, EXCLUDED.last_event_seq),
			last_event_at=EXCLUDED.last_event_at`,
		event.RunID, agentID, event.CommandID, event.ProjectID, event.SessionID, event.Seq, event.At)
	return err
}

func (s *Store) saveRunEventGap(ctx context.Context, agentID, runID string, expected, observed uint64, kind string) error {
	id := runEventGapID(agentID, runID, expected, observed, kind)
	_, err := s.pool.Exec(ctx, `INSERT INTO run_event_gaps (id, agent_id, run_id, expected_seq, observed_seq, kind, status, detected_at)
		VALUES ($1,$2,$3,$4,$5,$6,'open',now())
		ON CONFLICT (id) DO NOTHING`, id, agentID, runID, expected, observed, kind)
	return err
}

func (s *Store) resolveRunEventGaps(ctx context.Context, agentID, runID string, seq uint64) error {
	_, err := s.pool.Exec(ctx, `UPDATE run_event_gaps
		SET status='resolved', resolved_at=now()
		WHERE agent_id=$1 AND run_id=$2 AND status='open' AND expected_seq <= $3 AND observed_seq > $3`,
		agentID, runID, seq)
	return err
}

func runEventGapID(agentID, runID string, expected, observed uint64, kind string) string {
	sum := sha256.Sum256([]byte(agentID + "\x00" + runID + "\x00" + kind + "\x00" + strconvUint(expected) + "\x00" + strconvUint(observed)))
	return "gap_" + hex.EncodeToString(sum[:])[:32]
}

func strconvUint(value uint64) string {
	if value == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for value > 0 {
		i--
		buf[i] = byte('0' + value%10)
		value /= 10
	}
	return string(buf[i:])
}

func (s *Store) projectApproval(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if event.Kind != "approval.requested" {
		return nil
	}
	var request protocol.ApprovalRequest
	if len(event.Payload) > 0 {
		_ = json.Unmarshal(event.Payload, &request)
	}
	if request.ID == "" {
		request.ID = event.EventID
	}
	request.RunID = storeFirstNonEmpty(request.RunID, event.RunID)
	request.ProjectID = storeFirstNonEmpty(request.ProjectID, event.ProjectID)
	request.SessionID = storeFirstNonEmpty(request.SessionID, event.SessionID)
	if request.At.IsZero() {
		request.At = event.At
	}
	if len(request.Payload) == 0 {
		request.Payload = event.Payload
	}
	return s.SaveApprovalRequest(ctx, agentID, request)
}

func (s *Store) projectRunState(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if event.RunID == "" {
		return nil
	}
	status := ""
	statusReason := ""
	finished := false
	switch event.Kind {
	case "run.started":
		status = "running"
	case "run.done", "run.completed":
		status = "done"
		finished = true
	case "run.error", "run.failed":
		status = "error"
		statusReason = runEventReason(event)
		finished = true
	case "run.stopped":
		status = "stopped"
		finished = true
	case "run.awaiting_approval":
		status = "awaiting_approval"
	}
	if status == "" {
		return nil
	}
	startedAt := event.At
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finished {
		_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, status_reason, last_event_seq, last_event_at, started_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10) ON CONFLICT (agent_id, id) DO UPDATE SET status=EXCLUDED.status, status_reason=COALESCE(NULLIF(EXCLUDED.status_reason, ''), runs.status_reason), command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runs.project_id), session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runs.session_id), last_event_seq=GREATEST(runs.last_event_seq, EXCLUDED.last_event_seq), last_event_at=EXCLUDED.last_event_at, finished_at=EXCLUDED.finished_at`, event.RunID, agentID, event.CommandID, event.ProjectID, event.SessionID, status, statusReason, event.Seq, startedAt, startedAt)
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, status_reason, last_event_seq, last_event_at, started_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (agent_id, id) DO UPDATE SET status=EXCLUDED.status, status_reason=COALESCE(NULLIF(EXCLUDED.status_reason, ''), runs.status_reason), command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runs.project_id), session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runs.session_id), last_event_seq=GREATEST(runs.last_event_seq, EXCLUDED.last_event_seq), last_event_at=EXCLUDED.last_event_at`, event.RunID, agentID, event.CommandID, event.ProjectID, event.SessionID, status, statusReason, event.Seq, startedAt, startedAt)
	return err
}

func (s *Store) projectCommandStateFromRunEvent(ctx context.Context, agentID string, event protocol.RunEvent, audit bool) error {
	if event.RunID == "" {
		return nil
	}
	status := ""
	reason := ""
	switch event.Kind {
	case "run.started":
		status = "running"
		reason = "run started"
	case "run.awaiting_approval":
		status = "awaiting_approval"
		reason = runEventReason(event)
		if reason == "" {
			reason = "run awaiting approval"
		}
	case "run.done", "run.completed":
		status = "done"
		reason = "run completed"
	case "run.error", "run.failed":
		status = "failed"
		reason = runEventReason(event)
		if reason == "" {
			reason = "run failed"
		}
	case "run.stopped":
		status = "stopped"
		reason = "run stopped"
	default:
		return nil
	}
	commandID := strings.TrimSpace(event.CommandID)
	if commandID == "" {
		if err := s.pool.QueryRow(ctx, `SELECT COALESCE(command_id, '') FROM runs WHERE agent_id=$1 AND id=$2`, agentID, event.RunID).Scan(&commandID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
	}
	if commandID == "" {
		return nil
	}
	tag, err := s.pool.Exec(ctx, `UPDATE commands SET status=$1, status_reason=$2, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL
		WHERE agent_id=$3 AND id=$4
			AND (run_id IS NULL OR run_id='' OR run_id=$5)
			AND status NOT IN ('done','expired','failed','rejected','stopped')`, status, reason, agentID, commandID, event.RunID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if !audit {
		return nil
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "run", status, reason, protocol.Raw(event))
}

func runEventReason(event protocol.RunEvent) string {
	if len(event.Payload) == 0 {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return ""
	}
	for _, key := range []string{"error", "reason", "message", "status"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s *Store) projectRuntimeSession(ctx context.Context, agentID string, event protocol.RunEvent) error {
	switch event.Kind {
	case "session.created", "session.resumed", "session.updated", "run.status", "run.completed", "run.done":
	default:
		return nil
	}
	var runtime protocol.RuntimeSession
	if len(event.Payload) > 0 {
		_ = json.Unmarshal(event.Payload, &runtime)
	}
	if runtime.Runtime == "" {
		runtime.Runtime = "openai.responses"
	}
	if runtime.ProjectID == "" {
		runtime.ProjectID = event.ProjectID
	}
	if runtime.SessionID == "" {
		runtime.SessionID = event.SessionID
	}
	if runtime.LastRunID == "" {
		runtime.LastRunID = event.RunID
	}
	if runtime.NativeSessionID == "" && event.SessionID != "" {
		runtime.NativeSessionID = event.SessionID
	}
	if runtime.SessionID == "" && runtime.NativeSessionID == "" && runtime.LastResponseID == "" {
		return nil
	}
	return s.UpsertRuntimeSession(ctx, agentID, runtime)
}

func storeFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (s *Store) SaveHeartbeat(ctx context.Context, agentID, deviceID string, heartbeat protocol.Heartbeat) error {
	if _, err := s.pool.Exec(ctx, `INSERT INTO heartbeats (agent_id, device_id, status, payload, received_at) VALUES ($1,$2,$3,$4,now()) ON CONFLICT (agent_id) DO UPDATE SET device_id=EXCLUDED.device_id, status=EXCLUDED.status, payload=EXCLUDED.payload, received_at=now()`, agentID, deviceID, heartbeat.Status, jsonRaw(heartbeat)); err != nil {
		return err
	}
	return s.ReconcileHeartbeatRuns(ctx, agentID, heartbeat)
}

func (s *Store) SaveCommand(ctx context.Context, agentID string, command protocol.Command) (protocol.Command, error) {
	command = normalizeCommand(command)
	if strings.TrimSpace(command.IdempotencyKey) != "" {
		existing, ok, err := s.CommandByIdempotencyKey(ctx, agentID, command.IdempotencyKey)
		if err != nil {
			return protocol.Command{}, err
		}
		if ok {
			return existing.ToProtocol(), nil
		}
	}
	tag, err := s.pool.Exec(ctx, `INSERT INTO commands (id, agent_id, run_id, session_id, project_id, kind, mode, payload, status, deadline_at, expires_at, idempotency_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'queued',$9,$10,$11) ON CONFLICT (agent_id, id) DO NOTHING`, command.CommandID, agentID, command.RunID, command.SessionID, command.ProjectID, command.Kind, command.Mode, jsonRaw(command.Payload), nullTime(command.DeadlineAt), nullTime(command.ExpiresAt), nullString(command.IdempotencyKey))
	if err != nil {
		return protocol.Command{}, err
	}
	if tag.RowsAffected() == 0 {
		existing, ok, err := s.CommandByID(ctx, agentID, command.CommandID)
		if err != nil {
			return protocol.Command{}, err
		}
		if ok {
			return existing.ToProtocol(), nil
		}
		return protocol.Command{}, nil
	}
	if err := s.SaveCommandAttempt(ctx, agentID, command.CommandID, "queued", "queued", "command queued", nil); err != nil {
		return protocol.Command{}, err
	}
	if !storeCommandCreatesRun(command.Kind) {
		return command, nil
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, status_reason, started_at) VALUES ($1,$2,$3,$4,$5,'queued','command_queued',now()) ON CONFLICT (agent_id, id) DO UPDATE SET command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runs.project_id), session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runs.session_id)`, command.RunID, agentID, command.CommandID, command.ProjectID, command.SessionID)
	return command, err
}

func (s *Store) MarkCommandDispatched(ctx context.Context, agentID, commandID string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='dispatched', status_reason='', dispatched_at=now(), retry_count=retry_count+1, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatch_failed','failed','expired','dispatched')`, agentID, commandID); err != nil {
		return err
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "dispatch", "dispatched", "command written to agent transport", nil)
}

func (s *Store) MarkCommandFailed(ctx context.Context, agentID, commandID, reason string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='dispatch_failed', status_reason=$3, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed','failed','expired')`, agentID, commandID, reason); err != nil {
		return err
	}
	if err := s.releaseFailedApprovalDecisionCommand(ctx, agentID, commandID, reason); err != nil {
		return err
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "dispatch", "failed", reason, nil)
}

func (s *Store) MarkCommandExpired(ctx context.Context, agentID, commandID, reason string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='expired', status_reason=$3, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed','failed','expired')`, agentID, commandID, reason); err != nil {
		return err
	}
	if err := s.releaseFailedApprovalDecisionCommand(ctx, agentID, commandID, reason); err != nil {
		return err
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "expire", "expired", reason, nil)
}

func (s *Store) PrepareCommandRetry(ctx context.Context, agentID, commandID string, deadlineAt, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='queued', status_reason='retry requested', deadline_at=$3, expires_at=$4, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed','failed','expired')`, agentID, commandID, deadlineAt.UTC(), expiresAt.UTC()); err != nil {
		return err
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "retry", "queued", "retry requested", protocol.Raw(map[string]any{"deadline_at": deadlineAt.UTC(), "expires_at": expiresAt.UTC()}))
}

func (s *Store) MarkCommandAck(ctx context.Context, agentID string, ack protocol.Ack) error {
	if ack.CommandID == "" {
		return nil
	}
	command, found, err := s.CommandByID(ctx, agentID, ack.CommandID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	status := commandStatusFromAck(ack)
	accepted := isAcceptedAckStatus(status)
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET
			status=CASE WHEN $6 AND status IN ('running','awaiting_approval') THEN status ELSE $1 END,
			status_reason=CASE WHEN $6 AND status IN ('running','awaiting_approval') THEN status_reason ELSE $2 END,
			acked_at=now(),
			dispatch_claimed_by=NULL,
			dispatch_claimed_at=NULL,
			dispatch_claimed_until=NULL
		WHERE agent_id=$3 AND id=$4
			AND (run_id IS NULL OR run_id='' OR $5='' OR run_id=$5)
			AND status NOT IN ('done','expired','failed','rejected','stopped')`, status, ack.Message, agentID, ack.CommandID, ack.RunID, accepted); err != nil {
		return err
	}
	if !accepted {
		if err := s.releaseFailedApprovalDecisionCommand(ctx, agentID, ack.CommandID, ack.Message); err != nil {
			return err
		}
	}
	if err := s.SaveCommandAttempt(ctx, agentID, ack.CommandID, "ack", status, ack.Message, protocol.Raw(ack)); err != nil {
		return err
	}
	if accepted && strings.TrimPrefix(command.Kind, "command.") == "approval_decision" {
		var decision protocol.ApprovalDecision
		if err := json.Unmarshal(command.Payload, &decision); err == nil && decision.ApprovalID != "" {
			if decision.DecidedAt.IsZero() {
				decision.DecidedAt = time.Now().UTC()
			}
			return s.DecideApproval(ctx, decision.ApprovalID, decision)
		}
	}
	return nil
}

func (s *Store) ExpireCommands(ctx context.Context, agentID string) error {
	rows, err := s.pool.Query(ctx, `SELECT id FROM commands WHERE agent_id=$1 AND status IN ('queued','dispatch_failed','dispatched') AND expires_at IS NOT NULL AND expires_at < now()`, agentID)
	if err != nil {
		return err
	}
	expired := []string{}
	for rows.Next() {
		var commandID string
		if err := rows.Scan(&commandID); err != nil {
			rows.Close()
			return err
		}
		expired = append(expired, commandID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='expired', status_reason='command expired before agent accepted it', dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND status IN ('queued','dispatch_failed','dispatched') AND expires_at IS NOT NULL AND expires_at < now()`, agentID); err != nil {
		return err
	}
	for _, commandID := range expired {
		if err := s.releaseFailedApprovalDecisionCommand(ctx, agentID, commandID, "command expired before agent accepted it"); err != nil {
			return err
		}
		if err := s.SaveCommandAttempt(ctx, agentID, commandID, "expire", "expired", "command expired before agent accepted it", nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ReconcileHeartbeatRuns(ctx context.Context, agentID string, heartbeat protocol.Heartbeat) error {
	running := map[string]struct{}{}
	for _, runID := range heartbeat.RunningRuns {
		if strings.TrimSpace(runID) != "" {
			running[strings.TrimSpace(runID)] = struct{}{}
		}
	}
	for runID := range running {
		if _, err := s.pool.Exec(ctx, `UPDATE runs SET status='running', status_reason='reported_by_agent_heartbeat', last_event_seq=GREATEST(last_event_seq, $3), last_event_at=$4 WHERE agent_id=$1 AND id=$2 AND status IN ('queued','accepted','running','awaiting_approval','stale')`, agentID, runID, heartbeat.LastEventSeq, heartbeat.At); err != nil {
			return err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM runs WHERE agent_id=$1 AND status IN ('running','awaiting_approval')`, agentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	stale := []string{}
	for rows.Next() {
		var runID string
		if err := rows.Scan(&runID); err != nil {
			return err
		}
		if _, ok := running[runID]; !ok {
			stale = append(stale, runID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, runID := range stale {
		if _, err := s.pool.Exec(ctx, `UPDATE runs SET status='stale', status_reason='missing_from_agent_heartbeat', last_event_seq=GREATEST(last_event_seq, $3), last_event_at=$4 WHERE agent_id=$1 AND id=$2 AND status IN ('running','awaiting_approval')`, agentID, runID, heartbeat.LastEventSeq, heartbeat.At); err != nil {
			return err
		}
	}
	return s.ExpireCommands(ctx, agentID)
}

func (s *Store) releaseFailedApprovalDecisionCommand(ctx context.Context, agentID, commandID, reason string) error {
	command, found, err := s.CommandByID(ctx, agentID, commandID)
	if err != nil || !found {
		return err
	}
	if strings.TrimPrefix(command.Kind, "command.") != "approval_decision" {
		return nil
	}
	return s.ReleaseApprovalDecisionCommand(ctx, agentID, commandID, reason)
}

func normalizeCommand(command protocol.Command) protocol.Command {
	now := time.Now().UTC()
	if command.DeadlineAt.IsZero() {
		command.DeadlineAt = now.Add(10 * time.Minute)
	}
	if command.ExpiresAt.IsZero() {
		command.ExpiresAt = command.DeadlineAt.Add(50 * time.Minute)
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	return command
}

func commandStatusFromAck(ack protocol.Ack) string {
	switch strings.ToLower(strings.TrimSpace(ack.Status)) {
	case "ok", "accepted", "duplicate":
		return "accepted"
	case "rejected":
		return "rejected"
	case "failed", "error":
		return "failed"
	default:
		if strings.TrimSpace(ack.Status) == "" {
			return "acked"
		}
		return strings.ToLower(strings.TrimSpace(ack.Status))
	}
}

func isAcceptedAckStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted", "acked", "ok":
		return true
	default:
		return false
	}
}

func storeCommandCreatesRun(kind string) bool {
	switch strings.TrimPrefix(kind, "command.") {
	case "new_session", "resume", "send":
		return true
	default:
		return false
	}
}
