package store

import (
	"context"
	"time"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) SaveRunEvent(ctx context.Context, agentID, deviceID string, event protocol.RunEvent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO run_events (id, agent_id, device_id, run_id, session_id, project_id, kind, payload, event_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (id) DO NOTHING`, event.EventID, agentID, deviceID, event.RunID, event.SessionID, event.ProjectID, event.Kind, jsonRaw(event.Payload), event.At)
	if err != nil {
		return err
	}
	return s.projectRunState(ctx, agentID, event)
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
	}
	if status == "" {
		return nil
	}
	startedAt := event.At
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if finished {
		_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, project_id, session_id, status, started_at, finished_at) VALUES ($1,$2,$3,$4,$5,$6,now()) ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, project_id=EXCLUDED.project_id, session_id=EXCLUDED.session_id, finished_at=now()`, event.RunID, agentID, event.ProjectID, event.SessionID, status, startedAt)
		return err
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO runs (id, agent_id, project_id, session_id, status, started_at) VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT (id) DO UPDATE SET status=EXCLUDED.status, project_id=EXCLUDED.project_id, session_id=EXCLUDED.session_id`, event.RunID, agentID, event.ProjectID, event.SessionID, status, startedAt)
	return err
}

func (s *Store) SaveHeartbeat(ctx context.Context, agentID, deviceID string, heartbeat protocol.Heartbeat) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO heartbeats (agent_id, device_id, status, payload, received_at) VALUES ($1,$2,$3,$4,now()) ON CONFLICT (agent_id) DO UPDATE SET device_id=EXCLUDED.device_id, status=EXCLUDED.status, payload=EXCLUDED.payload, received_at=now()`, agentID, deviceID, heartbeat.Status, jsonRaw(heartbeat))
	return err
}

func (s *Store) SaveCommand(ctx context.Context, agentID string, command protocol.Command) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO commands (id, agent_id, run_id, session_id, project_id, kind, payload, status) VALUES ($1,$2,$3,$4,$5,$6,$7,'queued') ON CONFLICT (id) DO NOTHING`, command.CommandID, agentID, command.RunID, command.SessionID, command.ProjectID, command.Kind, jsonRaw(command.Payload))
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
