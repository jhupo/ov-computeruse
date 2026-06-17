package store

import (
	"context"
	"encoding/json"
	"time"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) SaveRunEvent(ctx context.Context, agentID, deviceID string, event protocol.RunEvent) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO run_events (id, agent_id, device_id, run_id, command_id, session_id, project_id, seq, kind, payload, event_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (id) DO NOTHING`, event.EventID, agentID, deviceID, event.RunID, event.CommandID, event.SessionID, event.ProjectID, event.Seq, event.Kind, jsonRaw(event.Payload), event.At)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
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
	return s.projectRunState(ctx, agentID, event)
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
	finished := false
	switch event.Kind {
	case "run.started":
		status = "running"
	case "run.done", "run.completed":
		status = "done"
		finished = true
	case "run.error", "run.failed":
		status = "error"
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
		_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, started_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,$7,now()) ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=EXCLUDED.project_id, session_id=EXCLUDED.session_id, finished_at=now()`, event.RunID, agentID, event.CommandID, event.ProjectID, event.SessionID, status, startedAt)
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, started_at) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=EXCLUDED.project_id, session_id=EXCLUDED.session_id`, event.RunID, agentID, event.CommandID, event.ProjectID, event.SessionID, status, startedAt)
	return err
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
	_, err := s.pool.Exec(ctx, `INSERT INTO heartbeats (agent_id, device_id, status, payload, received_at) VALUES ($1,$2,$3,$4,now()) ON CONFLICT (agent_id) DO UPDATE SET device_id=EXCLUDED.device_id, status=EXCLUDED.status, payload=EXCLUDED.payload, received_at=now()`, agentID, deviceID, heartbeat.Status, jsonRaw(heartbeat))
	return err
}

func (s *Store) SaveCommand(ctx context.Context, agentID string, command protocol.Command) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO commands (id, agent_id, run_id, session_id, project_id, kind, payload, status) VALUES ($1,$2,$3,$4,$5,$6,$7,'queued') ON CONFLICT (id) DO NOTHING`, command.CommandID, agentID, command.RunID, command.SessionID, command.ProjectID, command.Kind, jsonRaw(command.Payload))
	if err != nil {
		return err
	}
	if command.RunID == "" {
		return nil
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, started_at) VALUES ($1,$2,$3,$4,$5,'queued',now()) ON CONFLICT (id) DO UPDATE SET command_id=COALESCE(NULLIF(EXCLUDED.command_id, ''), runs.command_id), project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runs.project_id), session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runs.session_id)`, command.RunID, agentID, command.CommandID, command.ProjectID, command.SessionID)
	return err
}

func (s *Store) MarkCommandDispatched(ctx context.Context, agentID, commandID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE commands SET status='dispatched' WHERE agent_id=$1 AND id=$2 AND status IN ('queued','failed')`, agentID, commandID)
	return err
}

func (s *Store) MarkCommandFailed(ctx context.Context, agentID, commandID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE commands SET status='failed' WHERE agent_id=$1 AND id=$2 AND status='queued'`, agentID, commandID)
	return err
}

func (s *Store) MarkCommandAck(ctx context.Context, agentID string, ack protocol.Ack) error {
	if ack.CommandID == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE commands SET status=$1, acked_at=now() WHERE agent_id=$2 AND id=$3`, ack.Status, agentID, ack.CommandID)
	return err
}
