package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"ov-computeruse/server/internal/protocol"
)

type RuntimeTimelineItem struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	RunID      string          `json:"run_id"`
	SessionID  string          `json:"session_id,omitempty"`
	ProjectID  string          `json:"project_id,omitempty"`
	Seq        uint64          `json:"seq"`
	Runtime    string          `json:"runtime"`
	ThreadID   string          `json:"thread_id,omitempty"`
	TurnID     string          `json:"turn_id,omitempty"`
	ItemID     string          `json:"item_id,omitempty"`
	ItemType   string          `json:"item_type,omitempty"`
	Phase      string          `json:"phase,omitempty"`
	Kind       string          `json:"kind"`
	Role       string          `json:"role,omitempty"`
	Text       string          `json:"text,omitempty"`
	Status     string          `json:"status,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	EventAt    time.Time       `json:"event_at"`
	ReceivedAt time.Time       `json:"received_at,omitempty"`
}

func (s *Store) projectRuntimeTimeline(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if event.RunID == "" || protocol.IsUsageKind(event.Kind) {
		return nil
	}
	timeline := runtimeTimelineFromEvent(agentID, event)
	if timeline.Runtime == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO runtime_timeline (id, agent_id, run_id, session_id, project_id, seq, runtime, thread_id, turn_id, item_id, item_type, phase, kind, role, text, status, payload, event_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		ON CONFLICT (agent_id, run_id, seq, kind, item_id, phase) DO UPDATE SET
			session_id=COALESCE(NULLIF(EXCLUDED.session_id, ''), runtime_timeline.session_id),
			project_id=COALESCE(NULLIF(EXCLUDED.project_id, ''), runtime_timeline.project_id),
			runtime=EXCLUDED.runtime,
			thread_id=COALESCE(NULLIF(EXCLUDED.thread_id, ''), runtime_timeline.thread_id),
			turn_id=COALESCE(NULLIF(EXCLUDED.turn_id, ''), runtime_timeline.turn_id),
			item_type=COALESCE(NULLIF(EXCLUDED.item_type, ''), runtime_timeline.item_type),
			role=COALESCE(NULLIF(EXCLUDED.role, ''), runtime_timeline.role),
			text=COALESCE(NULLIF(EXCLUDED.text, ''), runtime_timeline.text),
			status=COALESCE(NULLIF(EXCLUDED.status, ''), runtime_timeline.status),
			payload=EXCLUDED.payload,
			event_at=EXCLUDED.event_at,
			received_at=now()`,
		timeline.ID, timeline.AgentID, timeline.RunID, timeline.SessionID, timeline.ProjectID, timeline.Seq, timeline.Runtime,
		timeline.ThreadID, timeline.TurnID, timeline.ItemID, timeline.ItemType, timeline.Phase, timeline.Kind, timeline.Role,
		timeline.Text, timeline.Status, jsonRaw(timeline.Payload), timeline.EventAt)
	return err
}

func runtimeTimelineFromEvent(agentID string, event protocol.RunEvent) RuntimeTimelineItem {
	threadID := payloadString(event.Payload, "thread_id", "native_session_id")
	turnID := payloadString(event.Payload, "turn_id")
	itemID := payloadString(event.Payload, "item_id", "tool_call_id", "id")
	itemType := payloadString(event.Payload, "item_type", "tool_name", "tool")
	phase := payloadString(event.Payload, "phase")
	status := payloadString(event.Payload, "status")
	text := payloadText(event.Payload)
	role := ""
	switch event.Kind {
	case "user.message":
		role = "user"
	case "assistant.message.delta", "assistant.message.done":
		role = "assistant"
	}
	runtime := payloadString(event.Payload, "runtime")
	if runtime == "" && (threadID != "" || turnID != "" || itemID != "" || strings.HasPrefix(status, "codex.")) {
		runtime = protocol.RuntimeCodexCLI
	}
	if runtime == "" {
		return RuntimeTimelineItem{}
	}
	return RuntimeTimelineItem{
		ID:        projectionID(agentID, event.RunID, strconvUint(event.Seq), event.Kind, itemID, phase, "runtime_timeline"),
		AgentID:   agentID,
		RunID:     event.RunID,
		SessionID: event.SessionID,
		ProjectID: event.ProjectID,
		Seq:       event.Seq,
		Runtime:   runtime,
		ThreadID:  threadID,
		TurnID:    turnID,
		ItemID:    itemID,
		ItemType:  itemType,
		Phase:     phase,
		Kind:      event.Kind,
		Role:      role,
		Text:      text,
		Status:    status,
		Payload:   event.Payload,
		EventAt:   eventTime(event),
	}
}

func (s *Store) ListRuntimeTimeline(ctx context.Context, agentID, runID string, afterSeq uint64, limit int) ([]RuntimeTimelineItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	rows, err := s.pool.Query(ctx, runtimeTimelineRunQuery(), agentID, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuntimeTimeline(rows)
}

func (s *Store) ListSessionRuntimeTimeline(ctx context.Context, agentID, sessionID string, limit int) ([]RuntimeTimelineItem, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, runtimeTimelineSessionQuery(), agentID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRuntimeTimeline(rows)
}

func runtimeTimelineRunQuery() string {
	return `SELECT id, agent_id, run_id, COALESCE(session_id, ''), COALESCE(project_id, ''), seq, runtime, COALESCE(thread_id, ''), COALESCE(turn_id, ''), COALESCE(item_id, ''), COALESCE(item_type, ''), COALESCE(phase, ''), kind, COALESCE(role, ''), COALESCE(text, ''), COALESCE(status, ''), payload, event_at, received_at
		FROM runtime_timeline
		WHERE agent_id=$1 AND run_id=$2 AND seq>$3
		ORDER BY seq ASC, received_at ASC
		LIMIT $4`
}

func runtimeTimelineSessionQuery() string {
	return `WITH live AS (
			SELECT id, agent_id, run_id, COALESCE(session_id, '') AS session_id, COALESCE(project_id, '') AS project_id, seq, runtime, COALESCE(thread_id, '') AS thread_id, COALESCE(turn_id, '') AS turn_id, COALESCE(item_id, '') AS item_id, COALESCE(item_type, '') AS item_type, COALESCE(phase, '') AS phase, kind, COALESCE(role, '') AS role, COALESCE(text, '') AS text, COALESCE(status, '') AS status, payload, event_at, received_at
			FROM runtime_timeline
			WHERE agent_id=$1 AND (session_id=$2 OR thread_id=$2)
		), history AS (
			SELECT hi.id, hi.agent_id, '' AS run_id, hi.session_id, COALESCE(rs.project_id, '') AS project_id, hi.item_index::BIGINT AS seq, 'codex.cli' AS runtime, COALESCE(NULLIF(rs.native_session_id, ''), hi.session_id) AS thread_id, '' AS turn_id, COALESCE(NULLIF(hi.source_event_id, ''), hi.id) AS item_id, hi.kind AS item_type, 'history.item' AS phase,
				CASE
					WHEN hi.kind='message' AND COALESCE(hi.role, '')='user' THEN 'user.message'
					WHEN hi.kind='message' THEN 'assistant.message.done'
					WHEN hi.kind='reasoning' THEN 'assistant.reasoning.done'
					WHEN hi.kind='tool.call' THEN 'tool.call.done'
					WHEN hi.kind='tool.output' THEN 'tool.output'
					WHEN hi.kind='approval.requested' THEN 'approval.requested'
					WHEN hi.kind='todo.list' THEN 'todo.list'
					WHEN hi.kind='error' THEN 'run.error'
					ELSE hi.kind
				END AS kind,
				COALESCE(hi.role, '') AS role, COALESCE(hi.text, '') AS text, '' AS status, hi.payload, COALESCE(hi.item_at, hi.received_at) AS event_at, hi.received_at
			FROM history_items hi
			LEFT JOIN LATERAL (
				SELECT project_id, native_session_id
				FROM runtime_sessions
				WHERE agent_id=hi.agent_id AND (session_id=hi.session_id OR native_session_id=hi.session_id) AND runtime='codex.cli'
				ORDER BY updated_at DESC
				LIMIT 1
			) rs ON true
			WHERE hi.agent_id=$1 AND hi.session_id=$2
				AND NOT EXISTS (
					SELECT 1 FROM live
					WHERE live.item_id=COALESCE(NULLIF(hi.source_event_id, ''), hi.id)
				)
		)
		SELECT id, agent_id, run_id, session_id, project_id, seq, runtime, thread_id, turn_id, item_id, item_type, phase, kind, role, text, status, payload, event_at, received_at FROM live
		UNION ALL
		SELECT id, agent_id, run_id, session_id, project_id, seq, runtime, thread_id, turn_id, item_id, item_type, phase, kind, role, text, status, payload, event_at, received_at FROM history
		ORDER BY event_at ASC, received_at ASC, run_id ASC, seq ASC
		LIMIT $3`
}

func scanRuntimeTimeline(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]RuntimeTimelineItem, error) {
	out := []RuntimeTimelineItem{}
	for rows.Next() {
		var item RuntimeTimelineItem
		var payload []byte
		if err := rows.Scan(&item.ID, &item.AgentID, &item.RunID, &item.SessionID, &item.ProjectID, &item.Seq, &item.Runtime, &item.ThreadID, &item.TurnID, &item.ItemID, &item.ItemType, &item.Phase, &item.Kind, &item.Role, &item.Text, &item.Status, &payload, &item.EventAt, &item.ReceivedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
