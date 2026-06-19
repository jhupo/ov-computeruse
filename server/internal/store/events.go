package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ov-computeruse/server/internal/protocol"
)

type RunEventSaveStatus string

const (
	RunEventInserted         RunEventSaveStatus = "inserted"
	RunEventDuplicate        RunEventSaveStatus = "duplicate"
	RunEventSequenceConflict RunEventSaveStatus = "seq_conflict"
	RunEventIDConflict       RunEventSaveStatus = "event_id_conflict"
	RunEventIgnored          RunEventSaveStatus = "ignored"
)

type RunEventSaveResult struct {
	Status RunEventSaveStatus `json:"status"`
}

type runEventConflict struct {
	Existing RunEventRecord
	Observed protocol.RunEvent
	DeviceID string
	Message  string
}

func (r RunEventSaveResult) ShouldBroadcast() bool {
	return r.Status == RunEventInserted
}

func (r RunEventSaveResult) AckStatus() string {
	switch r.Status {
	case RunEventDuplicate:
		return "duplicate"
	case RunEventSequenceConflict, RunEventIDConflict:
		return "conflict"
	case RunEventIgnored:
		return "ignored"
	default:
		return "acked"
	}
}

type ProjectionRebuildResult struct {
	AgentID      string    `json:"agent_id"`
	RunID        string    `json:"run_id"`
	EventCount   int       `json:"event_count"`
	LastEventSeq uint64    `json:"last_event_seq"`
	LastEventAt  time.Time `json:"last_event_at,omitempty"`
	RebuiltAt    time.Time `json:"rebuilt_at"`
}

func (s *Store) SaveRunEvent(ctx context.Context, agentID, deviceID string, event protocol.RunEvent) (RunEventSaveResult, error) {
	if skipRunEvent(event) {
		return RunEventSaveResult{Status: RunEventIgnored}, nil
	}
	if event.EventID == "" {
		event.EventID = protocol.NewID("evt")
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if err := s.validateRunEventOwnership(ctx, agentID, event, false); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.projectRuntimeSession(ctx, agentID, event); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.validateRunEventOwnership(ctx, agentID, event, true); err != nil {
		return RunEventSaveResult{}, err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO run_events (id, agent_id, device_id, run_id, command_id, session_id, project_id, seq, kind, payload, event_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, event.EventID, agentID, deviceID, event.RunID, event.CommandID, event.SessionID, event.ProjectID, event.Seq, event.Kind, jsonRaw(event.Payload), event.At)
	if err != nil {
		if isUniqueViolation(err) {
			return s.resolveRunEventInsertConflict(ctx, agentID, deviceID, event)
		}
		return RunEventSaveResult{}, err
	}
	if err := s.advanceRunEventCursor(ctx, agentID, event); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.projectApproval(ctx, agentID, event); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.projectRunEvent(ctx, agentID, event); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.projectRunState(ctx, agentID, event); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.closePendingApprovalsFromRunEvent(ctx, agentID, event); err != nil {
		return RunEventSaveResult{}, err
	}
	if err := s.projectCommandStateFromRunEvent(ctx, agentID, event, true); err != nil {
		return RunEventSaveResult{}, err
	}
	return RunEventSaveResult{Status: RunEventInserted}, nil
}

func (s *Store) resolveRunEventInsertConflict(ctx context.Context, agentID, deviceID string, event protocol.RunEvent) (RunEventSaveResult, error) {
	if existing, found, err := s.runEventByID(ctx, event.EventID); err != nil {
		return RunEventSaveResult{}, err
	} else if found {
		if existing.AgentID == agentID && runEventsEquivalent(existing, deviceID, event) {
			return RunEventSaveResult{Status: RunEventDuplicate}, nil
		}
		if err := s.saveRunEventConflict(ctx, agentID, event.RunID, event.Seq, "event_id_conflict", runEventConflict{
			Existing: existing,
			Observed: event,
			DeviceID: deviceID,
			Message:  "incoming event id matches an existing event with different content or owner",
		}); err != nil {
			return RunEventSaveResult{}, err
		}
		return RunEventSaveResult{Status: RunEventIDConflict}, nil
	}
	if strings.TrimSpace(event.RunID) == "" || event.Seq == 0 {
		return RunEventSaveResult{Status: RunEventIDConflict}, nil
	}
	existing, found, err := s.runEventByRunSeq(ctx, agentID, event.RunID, event.Seq)
	if err != nil {
		return RunEventSaveResult{}, err
	}
	if !found {
		return RunEventSaveResult{Status: RunEventIDConflict}, nil
	}
	if runEventsEquivalent(existing, deviceID, event) {
		return RunEventSaveResult{Status: RunEventDuplicate}, nil
	}
	if err := s.saveRunEventConflict(ctx, agentID, event.RunID, event.Seq, "seq_conflict", runEventConflict{
		Existing: existing,
		Observed: event,
		DeviceID: deviceID,
		Message:  "incoming run event has the same run sequence as a different stored event",
	}); err != nil {
		return RunEventSaveResult{}, err
	}
	return RunEventSaveResult{Status: RunEventSequenceConflict}, nil
}

func (s *Store) runEventByID(ctx context.Context, eventID string) (RunEventRecord, bool, error) {
	if strings.TrimSpace(eventID) == "" {
		return RunEventRecord{}, false, nil
	}
	row := s.pool.QueryRow(ctx, `SELECT id, agent_id, device_id, COALESCE(run_id, ''), COALESCE(command_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), seq, kind, payload, event_at, received_at
		FROM run_events
		WHERE id=$1`, eventID)
	event, err := scanRunEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunEventRecord{}, false, nil
	}
	if err != nil {
		return RunEventRecord{}, false, err
	}
	return event, true, nil
}

func (s *Store) runEventByRunSeq(ctx context.Context, agentID, runID string, seq uint64) (RunEventRecord, bool, error) {
	row := s.pool.QueryRow(ctx, `SELECT id, agent_id, device_id, COALESCE(run_id, ''), COALESCE(command_id, ''), COALESCE(session_id, ''), COALESCE(project_id, ''), seq, kind, payload, event_at, received_at
		FROM run_events
		WHERE agent_id=$1 AND run_id=$2 AND seq=$3`, agentID, runID, seq)
	event, err := scanRunEvent(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunEventRecord{}, false, nil
	}
	if err != nil {
		return RunEventRecord{}, false, err
	}
	return event, true, nil
}

type runEventScanner interface {
	Scan(dest ...any) error
}

func scanRunEvent(scanner runEventScanner) (RunEventRecord, error) {
	var event RunEventRecord
	var payload []byte
	if err := scanner.Scan(&event.ID, &event.AgentID, &event.DeviceID, &event.RunID, &event.CommandID, &event.SessionID, &event.ProjectID, &event.Seq, &event.Kind, &payload, &event.EventAt, &event.ReceivedAt); err != nil {
		return RunEventRecord{}, err
	}
	if len(payload) > 0 {
		event.Payload = append(json.RawMessage(nil), payload...)
	}
	return event, nil
}

func runEventsEquivalent(existing RunEventRecord, deviceID string, incoming protocol.RunEvent) bool {
	return existing.DeviceID == deviceID &&
		existing.RunID == incoming.RunID &&
		existing.CommandID == incoming.CommandID &&
		existing.SessionID == incoming.SessionID &&
		existing.ProjectID == incoming.ProjectID &&
		existing.Seq == incoming.Seq &&
		existing.Kind == incoming.Kind &&
		jsonEquivalent(existing.Payload, incoming.Payload)
}

func jsonEquivalent(a, b json.RawMessage) bool {
	if len(a) == 0 || bytes.Equal(a, []byte("null")) {
		a = nil
	}
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		b = nil
	}
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	var av any
	var bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return bytes.Equal(bytes.TrimSpace(a), bytes.TrimSpace(b))
	}
	ac, aErr := json.Marshal(av)
	bc, bErr := json.Marshal(bv)
	if aErr != nil || bErr != nil {
		return false
	}
	return bytes.Equal(ac, bc)
}

func payloadSHA256(payload json.RawMessage) string {
	if len(payload) == 0 || bytes.Equal(payload, []byte("null")) {
		return ""
	}
	var value any
	if json.Unmarshal(payload, &value) == nil {
		if canonical, err := json.Marshal(value); err == nil {
			sum := sha256.Sum256(canonical)
			return hex.EncodeToString(sum[:])
		}
	}
	sum := sha256.Sum256(bytes.TrimSpace(payload))
	return hex.EncodeToString(sum[:])
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *Store) validateRunEventOwnership(ctx context.Context, agentID string, event protocol.RunEvent, validateSession bool) error {
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
	if validateSession && strings.TrimSpace(event.SessionID) != "" {
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
		if skipRunEvent(event) {
			continue
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
	for _, event := range events {
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
		if err := s.closePendingApprovalsFromRunEvent(ctx, agentID, event); err != nil {
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

func (s *Store) saveRunEventConflict(ctx context.Context, agentID, runID string, seq uint64, kind string, conflict runEventConflict) error {
	if strings.TrimSpace(runID) == "" || seq == 0 {
		return nil
	}
	id := runEventGapID(agentID, runID, seq, seq, kind)
	details := protocol.Raw(map[string]any{
		"message":          conflict.Message,
		"existing_device":  conflict.Existing.DeviceID,
		"observed_device":  conflict.DeviceID,
		"existing_command": conflict.Existing.CommandID,
		"observed_command": conflict.Observed.CommandID,
		"existing_session": conflict.Existing.SessionID,
		"observed_session": conflict.Observed.SessionID,
		"existing_project": conflict.Existing.ProjectID,
		"observed_project": conflict.Observed.ProjectID,
	})
	_, err := s.pool.Exec(ctx, `INSERT INTO run_event_gaps (id, agent_id, run_id, expected_seq, observed_seq, kind, status, existing_event_id, observed_event_id, existing_kind, observed_kind, existing_payload_sha256, observed_payload_sha256, details, detected_at)
		VALUES ($1,$2,$3,$4,$5,$6,'open',$7,$8,$9,$10,$11,$12,$13,now())
		ON CONFLICT (id) DO UPDATE SET
			status='open',
			existing_event_id=COALESCE(run_event_gaps.existing_event_id, EXCLUDED.existing_event_id),
			observed_event_id=EXCLUDED.observed_event_id,
			existing_kind=COALESCE(run_event_gaps.existing_kind, EXCLUDED.existing_kind),
			observed_kind=EXCLUDED.observed_kind,
			existing_payload_sha256=COALESCE(run_event_gaps.existing_payload_sha256, EXCLUDED.existing_payload_sha256),
			observed_payload_sha256=EXCLUDED.observed_payload_sha256,
			details=EXCLUDED.details,
			resolved_at=NULL`,
		id, agentID, runID, seq, seq, kind,
		conflict.Existing.ID, conflict.Observed.EventID, conflict.Existing.Kind, conflict.Observed.Kind,
		payloadSHA256(conflict.Existing.Payload), payloadSHA256(conflict.Observed.Payload), jsonRaw(details))
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

func (s *Store) closePendingApprovalsFromRunEvent(ctx context.Context, agentID string, event protocol.RunEvent) error {
	switch event.Kind {
	case "run.done", "run.completed", "run.error", "run.failed", "run.stopped":
		return s.closePendingApprovalsForRun(ctx, agentID, event.RunID, "run finished: "+event.Kind)
	default:
		return nil
	}
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
	runtime, ok := runtimeSessionFromEvent(event)
	if !ok {
		return nil
	}
	if _, err := s.UpsertRuntimeSession(ctx, agentID, runtime); err != nil {
		return err
	}
	if strings.TrimSpace(event.RunID) == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE runs
		SET session_id=COALESCE(NULLIF($3, ''), session_id),
			project_id=COALESCE(NULLIF($4, ''), project_id)
		WHERE agent_id=$1 AND id=$2`, agentID, event.RunID, runtime.SessionID, runtime.ProjectID)
	return err
}

func runtimeSessionFromEvent(event protocol.RunEvent) (protocol.RuntimeSession, bool) {
	switch event.Kind {
	case "session.created", "session.resumed", "session.updated":
	default:
		return protocol.RuntimeSession{}, false
	}
	var runtime protocol.RuntimeSession
	if len(event.Payload) > 0 {
		_ = json.Unmarshal(event.Payload, &runtime)
	}
	if runtime.Runtime != protocol.RuntimeCodexCLI {
		return protocol.RuntimeSession{}, false
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
	if runtime.SessionID == "" && runtime.NativeSessionID == "" {
		return protocol.RuntimeSession{}, false
	}
	return runtime, true
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
			if !commandMatchesIdempotency(existing, command) {
				return protocol.Command{}, ErrCommandIdempotencyConflict
			}
			return existing.ToProtocol(), nil
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return protocol.Command{}, err
	}
	defer tx.Rollback(ctx)
	if storeCommandCreatesRun(command.Kind) {
		if err := s.lockCommandSession(ctx, tx, agentID, command.SessionID); err != nil {
			return protocol.Command{}, err
		}
		if err := s.ensureSessionHasNoActiveRun(ctx, tx, agentID, command); err != nil {
			return protocol.Command{}, err
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO commands (id, agent_id, run_id, session_id, project_id, kind, mode, payload, status, deadline_at, expires_at, idempotency_key) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'queued',$9,$10,$11) ON CONFLICT (agent_id, id) DO NOTHING`, command.CommandID, agentID, command.RunID, command.SessionID, command.ProjectID, command.Kind, command.Mode, jsonRaw(command.Payload), nullTime(command.DeadlineAt), nullTime(command.ExpiresAt), nullString(command.IdempotencyKey))
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
	if err := s.saveCommandAttemptTx(ctx, tx, agentID, command.CommandID, "queued", "queued", "command queued", nil); err != nil {
		return protocol.Command{}, err
	}
	if !storeCommandCreatesRun(command.Kind) {
		if storeCommandStopsRun(command.Kind) {
			command, err = s.markStopRequestedTx(ctx, tx, agentID, command)
			if err != nil {
				return protocol.Command{}, err
			}
		}
		return command, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, status_reason, started_at) VALUES ($1,$2,$3,$4,$5,'queued','command_queued',now()) ON CONFLICT (agent_id, id) DO UPDATE SET command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runs.project_id), session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runs.session_id)`, command.RunID, agentID, command.CommandID, command.ProjectID, command.SessionID); err != nil {
		return protocol.Command{}, err
	}
	return command, tx.Commit(ctx)
}

func (s *Store) lockCommandSession(ctx context.Context, tx pgx.Tx, agentID, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0), hashtextextended($2, 0))`, agentID, sessionID)
	return err
}

func (s *Store) ensureSessionHasNoActiveRun(ctx context.Context, tx pgx.Tx, agentID string, command protocol.Command) error {
	if strings.TrimSpace(command.SessionID) == "" {
		return nil
	}
	var activeRunID string
	err := tx.QueryRow(ctx, `SELECT id
		FROM runs
		WHERE agent_id=$1
			AND session_id=$2
			AND id<>$3
			AND status IN ('queued','accepted','running','awaiting_approval','stopping')
		ORDER BY started_at DESC
		LIMIT 1
		FOR UPDATE`, agentID, command.SessionID, command.RunID).Scan(&activeRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(ErrSessionActive, errors.New(activeRunID))
}

func (s *Store) markStopRequestedTx(ctx context.Context, tx pgx.Tx, agentID string, command protocol.Command) (protocol.Command, error) {
	runID := strings.TrimSpace(command.RunID)
	if runID == "" && strings.TrimSpace(command.SessionID) != "" {
		err := tx.QueryRow(ctx, `SELECT id
			FROM runs
			WHERE agent_id=$1
				AND session_id=$2
				AND status IN ('queued','accepted','running','awaiting_approval','stale','stopping')
			ORDER BY started_at DESC
			LIMIT 1
			FOR UPDATE`, agentID, command.SessionID).Scan(&runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return command, s.completeStopCommandTx(ctx, tx, agentID, command, "", "no active run for session")
		}
		if err != nil {
			return protocol.Command{}, err
		}
		command.RunID = runID
		if _, err := tx.Exec(ctx, `UPDATE commands SET run_id=$3 WHERE agent_id=$1 AND id=$2`, agentID, command.CommandID, runID); err != nil {
			return protocol.Command{}, err
		}
	}
	if runID == "" {
		return command, s.completeStopCommandTx(ctx, tx, agentID, command, "", "no active run to stop")
	}
	locallyStopped, err := s.stopUndispatchedRunTx(ctx, tx, agentID, command, runID)
	if err != nil {
		return protocol.Command{}, err
	}
	if locallyStopped {
		return command, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE runs
		SET status='stopping',
			status_reason='stop requested',
			finished_at=NULL
		WHERE agent_id=$1
			AND id=$2
			AND status IN ('queued','accepted','running','awaiting_approval','stale','stopping')`, agentID, runID)
	if err != nil {
		return protocol.Command{}, err
	}
	if tag.RowsAffected() == 0 {
		return command, s.completeStopCommandTx(ctx, tx, agentID, command, runID, "run is not active")
	}
	return command, s.saveCommandAttemptTx(ctx, tx, agentID, command.CommandID, "stop", "stopping", "stop requested", protocol.Raw(map[string]any{"run_id": runID, "session_id": command.SessionID}))
}

func (s *Store) completeStopCommandTx(ctx context.Context, tx pgx.Tx, agentID string, command protocol.Command, runID, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "stop completed"
	}
	if _, err := tx.Exec(ctx, `UPDATE commands
		SET status='done',
			status_reason=$3,
			acked_at=COALESCE(acked_at, now()),
			dispatch_claimed_by=NULL,
			dispatch_claimed_at=NULL,
			dispatch_claimed_until=NULL,
			run_id=COALESCE(NULLIF($4, ''), run_id)
		WHERE agent_id=$1
			AND id=$2
			AND status NOT IN ('done','expired','failed','rejected','stopped')`,
		agentID, command.CommandID, reason, runID); err != nil {
		return err
	}
	return s.saveCommandAttemptTx(ctx, tx, agentID, command.CommandID, "stop", "done", reason, protocol.Raw(map[string]any{"run_id": runID, "session_id": command.SessionID, "local": true}))
}

func (s *Store) stopUndispatchedRunTx(ctx context.Context, tx pgx.Tx, agentID string, command protocol.Command, runID string) (bool, error) {
	var runCommandID string
	err := tx.QueryRow(ctx, `SELECT COALESCE(command_id, '')
		FROM runs
		WHERE agent_id=$1
			AND id=$2
			AND status='queued'
		FOR UPDATE`, agentID, runID).Scan(&runCommandID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(runCommandID) == "" {
		return false, nil
	}
	tag, err := tx.Exec(ctx, `UPDATE commands
		SET status='stopped',
			status_reason='stopped before dispatch',
			acked_at=COALESCE(acked_at, now()),
			dispatch_claimed_by=NULL,
			dispatch_claimed_at=NULL,
			dispatch_claimed_until=NULL
		WHERE agent_id=$1
			AND id=$2
			AND status IN ('queued','dispatch_failed')
			AND (dispatch_claimed_until IS NULL OR dispatch_claimed_until < now())`,
		agentID, runCommandID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE runs
		SET status='stopped',
			status_reason='stopped before dispatch',
			finished_at=COALESCE(finished_at, now())
		WHERE agent_id=$1
			AND id=$2
			AND status='queued'`, agentID, runID); err != nil {
		return false, err
	}
	reason := "run stopped before agent dispatch"
	if _, err := tx.Exec(ctx, `UPDATE commands
		SET status='done',
			status_reason=$3,
			acked_at=COALESCE(acked_at, now()),
			dispatch_claimed_by=NULL,
			dispatch_claimed_at=NULL,
			dispatch_claimed_until=NULL,
			run_id=COALESCE(NULLIF($4, ''), run_id)
		WHERE agent_id=$1
			AND id=$2
			AND status NOT IN ('done','expired','failed','rejected','stopped')`,
		agentID, command.CommandID, reason, runID); err != nil {
		return false, err
	}
	if err := s.saveCommandAttemptTx(ctx, tx, agentID, runCommandID, "stop", "stopped", reason, protocol.Raw(map[string]any{"run_id": runID, "stop_command_id": command.CommandID})); err != nil {
		return false, err
	}
	if err := s.saveCommandAttemptTx(ctx, tx, agentID, command.CommandID, "stop", "done", reason, protocol.Raw(map[string]any{"run_id": runID, "session_id": command.SessionID, "local": true})); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) MarkCommandDispatched(ctx context.Context, agentID, commandID string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE commands SET status='dispatched', status_reason='', dispatched_at=now(), retry_count=retry_count+1, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatch_failed','dispatched')`, agentID, commandID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "dispatch", "dispatched", "command written to agent transport", nil)
}

func (s *Store) MarkCommandDispatchFailed(ctx context.Context, agentID, commandID, reason string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE commands SET status='dispatch_failed', status_reason=$3, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed')`, agentID, commandID, reason)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "dispatch", "failed", reason, nil)
}

func (s *Store) MarkCommandFailed(ctx context.Context, agentID, commandID, reason string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='dispatch_failed', status_reason=$3, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed','failed','expired')`, agentID, commandID, reason); err != nil {
		return err
	}
	if err := s.markRunForCommandTerminal(ctx, agentID, commandID, "failed", reason); err != nil {
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
	if err := s.markRunForCommandTerminal(ctx, agentID, commandID, "expired", reason); err != nil {
		return err
	}
	if err := s.releaseFailedApprovalDecisionCommand(ctx, agentID, commandID, reason); err != nil {
		return err
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "expire", "expired", reason, nil)
}

func (s *Store) MarkCommandStopped(ctx context.Context, agentID, commandID, reason string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE commands SET status='stopped', status_reason=$3, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed','failed','expired','stopped')`, agentID, commandID, reason); err != nil {
		return err
	}
	if err := s.markRunForCommandTerminal(ctx, agentID, commandID, "stopped", reason); err != nil {
		return err
	}
	if err := s.releaseFailedApprovalDecisionCommand(ctx, agentID, commandID, reason); err != nil {
		return err
	}
	return s.SaveCommandAttempt(ctx, agentID, commandID, "dispatch", "stopped", reason, nil)
}

func (s *Store) PrepareCommandRetry(ctx context.Context, agentID, commandID string, deadlineAt, expiresAt time.Time) error {
	command, found, err := s.CommandByID(ctx, agentID, commandID)
	if err != nil || !found {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if storeCommandCreatesRun(command.Kind) {
		if err := s.lockCommandSession(ctx, tx, agentID, command.SessionID); err != nil {
			return err
		}
		if err := s.ensureSessionHasNoActiveRun(ctx, tx, agentID, command.ToProtocol()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE commands SET status='queued', status_reason='retry requested', deadline_at=$3, expires_at=$4, dispatch_claimed_by=NULL, dispatch_claimed_at=NULL, dispatch_claimed_until=NULL WHERE agent_id=$1 AND id=$2 AND status IN ('queued','dispatched','dispatch_failed','failed','expired')`, agentID, commandID, deadlineAt.UTC(), expiresAt.UTC()); err != nil {
		return err
	}
	if storeCommandCreatesRun(command.Kind) {
		if _, err := tx.Exec(ctx, `UPDATE runs SET status='queued', status_reason='retry_requested', finished_at=NULL WHERE agent_id=$1 AND command_id=$2 AND id=$3`, agentID, commandID, command.RunID); err != nil {
			return err
		}
	}
	if err := s.saveCommandAttemptTx(ctx, tx, agentID, commandID, "retry", "queued", "retry requested", protocol.Raw(map[string]any{"deadline_at": deadlineAt.UTC(), "expires_at": expiresAt.UTC()})); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	if storeCommandStopsRun(command.Kind) {
		return s.markStopCommandAck(ctx, agentID, command, ack)
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
		if err := s.markRunForCommandTerminal(ctx, agentID, ack.CommandID, status, ack.Message); err != nil {
			return err
		}
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
			return s.DecideApproval(ctx, agentID, decision.ApprovalID, decision)
		}
	}
	return nil
}

func (s *Store) markStopCommandAck(ctx context.Context, agentID string, command CommandRecord, ack protocol.Ack) error {
	status := commandStatusFromAck(ack)
	accepted := isAcceptedAckStatus(status)
	commandStatus := "stop_failed"
	reason := ack.Message
	if accepted {
		commandStatus = "done"
		reason = "stop accepted by agent"
	}
	if reason == "" {
		reason = commandStatus
	}
	runID := storeFirstNonEmpty(ack.RunID, command.RunID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE commands SET
			status=$1,
			status_reason=$2,
			acked_at=now(),
			dispatch_claimed_by=NULL,
			dispatch_claimed_at=NULL,
			dispatch_claimed_until=NULL,
			run_id=COALESCE(NULLIF($5, ''), run_id)
		WHERE agent_id=$3 AND id=$4
			AND status NOT IN ('done','expired','failed','rejected','stopped')`,
		commandStatus, reason, agentID, command.ID, runID); err != nil {
		return err
	}
	if runID != "" {
		if accepted {
			if _, err := tx.Exec(ctx, `UPDATE runs
				SET status='stopping',
					status_reason='stop accepted by agent',
					finished_at=NULL
				WHERE agent_id=$1
					AND id=$2
					AND status IN ('queued','accepted','running','awaiting_approval','stale','stopping')`, agentID, runID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(ctx, `UPDATE runs
				SET status='stop_failed',
					status_reason=$3
				WHERE agent_id=$1
					AND id=$2
					AND status='stopping'`, agentID, runID, reason); err != nil {
				return err
			}
		}
	}
	if err := s.saveCommandAttemptTx(ctx, tx, agentID, command.ID, "ack", commandStatus, reason, protocol.Raw(ack)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) closePendingApprovalsForRun(ctx context.Context, agentID, runID, reason string) error {
	if strings.TrimSpace(runID) == "" {
		return nil
	}
	if strings.TrimSpace(reason) == "" {
		reason = "run finished"
	}
	_, err := s.pool.Exec(ctx, `UPDATE approval_requests
		SET status='cancelled',
			decision_reason=$3,
			decided_at=COALESCE(decided_at, now())
		WHERE agent_id=$1
			AND run_id=$2
			AND status='pending'`, agentID, runID, reason)
	return err
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
		if err := s.markRunForCommandTerminal(ctx, agentID, commandID, "expired", "command expired before agent accepted it"); err != nil {
			return err
		}
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
		if _, err := s.pool.Exec(ctx, `UPDATE runs SET status='running', status_reason='reported_by_agent_heartbeat', last_event_seq=GREATEST(last_event_seq, $3), last_event_at=$4 WHERE agent_id=$1 AND id=$2 AND status IN ('queued','accepted','running','awaiting_approval','stale') AND status <> 'stopping'`, agentID, runID, heartbeat.LastEventSeq, heartbeat.At); err != nil {
			return err
		}
	}
	rows, err := s.pool.Query(ctx, `SELECT id, status FROM runs WHERE agent_id=$1 AND status IN ('running','awaiting_approval','stopping')`, agentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	stale := []string{}
	for rows.Next() {
		var runID string
		var status string
		if err := rows.Scan(&runID, &status); err != nil {
			return err
		}
		if _, ok := running[runID]; !ok && heartbeatMissingRunCanBecomeStale(status) {
			stale = append(stale, runID)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, runID := range stale {
		if _, err := s.pool.Exec(ctx, `UPDATE runs SET status='stale', status_reason='missing_from_agent_heartbeat', last_event_seq=GREATEST(last_event_seq, $3), last_event_at=$4 WHERE agent_id=$1 AND id=$2 AND status IN ('running','awaiting_approval','stopping')`, agentID, runID, heartbeat.LastEventSeq, heartbeat.At); err != nil {
			return err
		}
	}
	return s.ExpireCommands(ctx, agentID)
}

func heartbeatMissingRunCanBecomeStale(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "awaiting_approval", "stopping":
		return true
	default:
		return false
	}
}

func (s *Store) markRunForCommandTerminal(ctx context.Context, agentID, commandID, status, reason string) error {
	if strings.TrimSpace(commandID) == "" {
		return nil
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "expired", "rejected", "stopped":
	default:
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE runs
		SET status=$3,
			status_reason=COALESCE(NULLIF($4, ''), status_reason),
			finished_at=COALESCE(finished_at, now())
		WHERE agent_id=$1
			AND command_id=$2
			AND (status IN ('queued','accepted') OR ($3='stopped' AND status='stopping'))`, agentID, commandID, strings.ToLower(strings.TrimSpace(status)), reason)
	return err
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
	if command.ExpiresAt.IsZero() {
		command.ExpiresAt = now.Add(60 * time.Minute)
	}
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	return command
}

func commandMatchesIdempotency(existing CommandRecord, incoming protocol.Command) bool {
	return strings.TrimSpace(existing.Kind) == strings.TrimSpace(incoming.Kind) &&
		strings.TrimSpace(existing.RunID) == strings.TrimSpace(incoming.RunID) &&
		strings.TrimSpace(existing.SessionID) == strings.TrimSpace(incoming.SessionID) &&
		strings.TrimSpace(existing.ProjectID) == strings.TrimSpace(incoming.ProjectID) &&
		strings.TrimSpace(existing.Mode) == strings.TrimSpace(incoming.Mode) &&
		jsonEquivalent(existing.Payload, incoming.Payload)
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

func storeCommandStopsRun(kind string) bool {
	return strings.TrimPrefix(kind, "command.") == "stop"
}
