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

type HistoryItem struct {
	ID            string          `json:"id"`
	AgentID       string          `json:"agent_id"`
	SessionID     string          `json:"session_id"`
	Index         int             `json:"index"`
	Role          string          `json:"role,omitempty"`
	Kind          string          `json:"kind"`
	Text          string          `json:"text,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
	Source        string          `json:"source,omitempty"`
	SourceEventID string          `json:"source_event_id,omitempty"`
	At            time.Time       `json:"at,omitempty"`
	ReceivedAt    time.Time       `json:"received_at,omitempty"`
}

type ConversationItem struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	SessionID  string          `json:"session_id"`
	RunID      string          `json:"run_id,omitempty"`
	Index      int             `json:"index,omitempty"`
	SeqStart   uint64          `json:"seq_start,omitempty"`
	Role       string          `json:"role,omitempty"`
	Kind       string          `json:"kind"`
	Text       string          `json:"text,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Source     string          `json:"source"`
	At         time.Time       `json:"at,omitempty"`
	ReceivedAt time.Time       `json:"received_at,omitempty"`
}

type RunMessage struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	RunID      string          `json:"run_id"`
	SeqStart   uint64          `json:"seq_start"`
	SeqEnd     uint64          `json:"seq_end,omitempty"`
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	Status     string          `json:"status"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
}

type RunStep struct {
	ID         string          `json:"id"`
	AgentID    string          `json:"agent_id"`
	RunID      string          `json:"run_id"`
	SeqStart   uint64          `json:"seq_start"`
	SeqEnd     uint64          `json:"seq_end,omitempty"`
	Kind       string          `json:"kind"`
	Title      string          `json:"title,omitempty"`
	Status     string          `json:"status"`
	Payload    json.RawMessage `json:"payload,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
}

type ToolCall struct {
	ID                string          `json:"id"`
	AgentID           string          `json:"agent_id"`
	RunID             string          `json:"run_id"`
	SeqStart          uint64          `json:"seq_start"`
	SeqEnd            uint64          `json:"seq_end,omitempty"`
	ToolCallID        string          `json:"tool_call_id,omitempty"`
	ToolName          string          `json:"tool_name,omitempty"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
	Output            json.RawMessage `json:"output,omitempty"`
	Status            string          `json:"status"`
	ApprovalRequestID string          `json:"approval_request_id,omitempty"`
	StartedAt         time.Time       `json:"started_at"`
	FinishedAt        time.Time       `json:"finished_at,omitempty"`
}

func (s *Store) SaveHistoryItem(ctx context.Context, item HistoryItem) error {
	if protocol.IsUsageKind(item.Kind) {
		return nil
	}
	if item.ID == "" {
		item.ID = projectionID(item.AgentID, item.SessionID, strconv.Itoa(item.Index), item.Kind, item.SourceEventID)
	}
	if item.At.IsZero() {
		item.At = time.Now().UTC()
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO history_items (id, agent_id, session_id, item_index, role, kind, text, payload, source, source_event_id, item_at, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now())
		ON CONFLICT (agent_id, session_id, item_index, kind) DO UPDATE SET role=EXCLUDED.role, text=EXCLUDED.text, payload=EXCLUDED.payload, source=EXCLUDED.source, source_event_id=EXCLUDED.source_event_id, item_at=EXCLUDED.item_at, received_at=now()`,
		item.ID, item.AgentID, item.SessionID, item.Index, item.Role, item.Kind, item.Text, jsonRaw(item.Payload), item.Source, item.SourceEventID, item.At)
	return err
}

func (s *Store) projectHistoryMessage(ctx context.Context, agentID string, message protocol.HistoryMessage) error {
	if message.SessionID == "" || message.Text == "" {
		return nil
	}
	return s.SaveHistoryItem(ctx, HistoryItem{
		AgentID:   agentID,
		SessionID: message.SessionID,
		Index:     message.Index,
		Role:      message.Role,
		Kind:      "message",
		Text:      message.Text,
		Payload:   protocol.Raw(message),
		Source:    "codex.history",
		At:        message.At,
	})
}

func (s *Store) projectRunEvent(ctx context.Context, agentID string, event protocol.RunEvent) error {
	if event.RunID == "" {
		return nil
	}
	if protocol.IsUsageKind(event.Kind) {
		return nil
	}
	switch event.Kind {
	case "assistant.message.delta":
		return s.appendAssistantRunMessage(ctx, agentID, event)
	case "assistant.message.done":
		return s.finishAssistantRunMessage(ctx, agentID, event)
	case "user.message":
		return s.upsertRunMessage(ctx, agentID, event, "user", "done", true)
	case "tool.call.started", "tool.call.delta", "tool.call.done", "tool.output":
		return s.upsertToolCall(ctx, agentID, event)
	case "approval.requested":
		if err := s.upsertRunStep(ctx, agentID, event, "approval", "Approval requested", "pending", false); err != nil {
			return err
		}
		return s.upsertToolCall(ctx, agentID, event)
	case "terminal.output":
		if payloadString(event.Payload, "tool_call_id", "call_id", "id") != "" {
			if err := s.upsertToolCall(ctx, agentID, event); err != nil {
				return err
			}
		}
		return s.upsertRunStep(ctx, agentID, event, event.Kind, stepTitle(event), stepStatus(event), true)
	case "diff.created", "run.status", "session.created", "session.updated", "session.resumed":
		kind, title, status := statusStepProjection(event)
		if kind != "" {
			return s.upsertRunStep(ctx, agentID, event, kind, title, status, true)
		}
		return s.upsertRunStep(ctx, agentID, event, event.Kind, stepTitle(event), stepStatus(event), true)
	case "run.started":
		return s.upsertRunStep(ctx, agentID, event, "run", "Run started", "running", false)
	case "run.done", "run.completed":
		return s.upsertRunStep(ctx, agentID, event, "run", "Run completed", "done", true)
	case "run.error", "run.failed":
		return s.upsertRunStep(ctx, agentID, event, "run", "Run failed", "error", true)
	case "run.stopped":
		return s.upsertRunStep(ctx, agentID, event, "run", "Run stopped", "stopped", true)
	default:
		return s.upsertRunStep(ctx, agentID, event, event.Kind, stepTitle(event), "done", true)
	}
}

func (s *Store) appendAssistantRunMessage(ctx context.Context, agentID string, event protocol.RunEvent) error {
	content := payloadText(event.Payload)
	if content == "" {
		return nil
	}
	existingID, existingContent, ok, err := s.lastRunMessage(ctx, agentID, event.RunID, "assistant")
	if err != nil {
		return err
	}
	if ok {
		_, err := s.pool.Exec(ctx, `UPDATE run_messages SET seq_end=$1, content=$2, payload=$3, status='streaming' WHERE id=$4 AND agent_id=$5`,
			event.Seq, existingContent+content, jsonRaw(event.Payload), existingID, agentID)
		return err
	}
	return s.upsertRunMessage(ctx, agentID, event, "assistant", "streaming", false)
}

func (s *Store) finishAssistantRunMessage(ctx context.Context, agentID string, event protocol.RunEvent) error {
	content := payloadText(event.Payload)
	existingID, _, ok, err := s.lastRunMessage(ctx, agentID, event.RunID, "assistant")
	if err != nil {
		return err
	}
	if ok {
		_, err := s.pool.Exec(ctx, `UPDATE run_messages SET seq_end=$1, content=COALESCE(NULLIF($2, ''), content), payload=$3, status='done', finished_at=$4 WHERE id=$5 AND agent_id=$6`,
			event.Seq, content, jsonRaw(event.Payload), eventTime(event), existingID, agentID)
		return err
	}
	return s.upsertRunMessage(ctx, agentID, event, "assistant", "done", true)
}

func (s *Store) lastRunMessage(ctx context.Context, agentID, runID, role string) (string, string, bool, error) {
	var id string
	var content string
	err := s.pool.QueryRow(ctx, `SELECT id, COALESCE(content, '') FROM run_messages WHERE agent_id=$1 AND run_id=$2 AND role=$3 ORDER BY seq_start DESC LIMIT 1`, agentID, runID, role).Scan(&id, &content)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	return id, content, true, nil
}

func (s *Store) upsertRunMessage(ctx context.Context, agentID string, event protocol.RunEvent, role, status string, finished bool) error {
	content := payloadText(event.Payload)
	id := projectionID(agentID, event.RunID, strconv.FormatUint(event.Seq, 10), "message", role)
	finishedAt := nullableTime(event.At, finished)
	_, err := s.pool.Exec(ctx, `INSERT INTO run_messages (id, agent_id, run_id, seq_start, seq_end, role, content, payload, status, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (agent_id, run_id, seq_start, role) DO UPDATE SET seq_end=EXCLUDED.seq_end, content=COALESCE(NULLIF(EXCLUDED.content, ''), run_messages.content), payload=EXCLUDED.payload, status=EXCLUDED.status, finished_at=EXCLUDED.finished_at`,
		id, agentID, event.RunID, event.Seq, event.Seq, role, content, jsonRaw(event.Payload), status, eventTime(event), finishedAt)
	return err
}

func (s *Store) upsertRunStep(ctx context.Context, agentID string, event protocol.RunEvent, kind, title, status string, finished bool) error {
	id := projectionID(agentID, event.RunID, strconv.FormatUint(event.Seq, 10), "step", kind)
	finishedAt := nullableTime(event.At, finished)
	_, err := s.pool.Exec(ctx, `INSERT INTO run_steps (id, agent_id, run_id, seq_start, seq_end, kind, title, status, payload, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (agent_id, run_id, seq_start, kind) DO UPDATE SET seq_end=EXCLUDED.seq_end, title=EXCLUDED.title, status=EXCLUDED.status, payload=EXCLUDED.payload, finished_at=EXCLUDED.finished_at`,
		id, agentID, event.RunID, event.Seq, event.Seq, kind, title, status, jsonRaw(event.Payload), eventTime(event), finishedAt)
	return err
}

func (s *Store) upsertToolCall(ctx context.Context, agentID string, event protocol.RunEvent) error {
	toolCallID := payloadString(event.Payload, "tool_call_id", "call_id", "id")
	if toolCallID == "" {
		toolCallID = projectionID(agentID, event.RunID, strconv.FormatUint(event.Seq, 10), "tool", event.Kind)
	}
	toolName := payloadString(event.Payload, "tool_name", "name", "tool")
	status := toolStatus(event.Kind)
	arguments := payloadObject(event.Payload, "arguments", "args")
	output := payloadObject(event.Payload, "output", "result", "text")
	approvalID := payloadString(event.Payload, "approval_id")
	id := projectionID(agentID, event.RunID, toolCallID, "tool_call")
	finished := status == "done" || status == "output"
	finishedAt := nullableTime(event.At, finished)
	_, err := s.pool.Exec(ctx, `INSERT INTO tool_calls (id, agent_id, run_id, seq_start, seq_end, tool_call_id, tool_name, arguments, output, status, approval_request_id, started_at, finished_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (agent_id, run_id, tool_call_id) DO UPDATE SET seq_end=EXCLUDED.seq_end, tool_name=COALESCE(NULLIF(EXCLUDED.tool_name, ''), tool_calls.tool_name), arguments=COALESCE(EXCLUDED.arguments, tool_calls.arguments), output=COALESCE(EXCLUDED.output, tool_calls.output), status=EXCLUDED.status, approval_request_id=COALESCE(NULLIF(EXCLUDED.approval_request_id, ''), tool_calls.approval_request_id), finished_at=COALESCE(EXCLUDED.finished_at, tool_calls.finished_at)`,
		id, agentID, event.RunID, event.Seq, event.Seq, toolCallID, toolName, jsonRaw(arguments), jsonRaw(output), status, approvalID, eventTime(event), finishedAt)
	return err
}

func (s *Store) ListHistoryItems(ctx context.Context, agentID, sessionID string, afterIndex, limit int) ([]HistoryItem, error) {
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, session_id, item_index, COALESCE(role, ''), kind, COALESCE(text, ''), payload, COALESCE(source, ''), COALESCE(source_event_id, ''), item_at, received_at
		FROM history_items
		WHERE agent_id=$1 AND session_id=$2 AND item_index>$3 AND lower(trim(kind)) NOT IN ('usage','response.usage','token_usage','billing','cost')
		ORDER BY item_index ASC
		LIMIT $4`, agentID, sessionID, afterIndex, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []HistoryItem{}
	for rows.Next() {
		var item HistoryItem
		var payload []byte
		var at sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.SessionID, &item.Index, &item.Role, &item.Kind, &item.Text, &payload, &item.Source, &item.SourceEventID, &at, &item.ReceivedAt); err != nil {
			return nil, err
		}
		if at.Valid {
			item.At = at.Time
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListConversationItems(ctx context.Context, agentID, sessionID string, limit int) ([]ConversationItem, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `WITH history AS (
			SELECT id, agent_id, session_id, '' AS run_id, item_index, 0::BIGINT AS seq_start, COALESCE(role, '') AS role, kind, COALESCE(text, '') AS text, payload, COALESCE(source, 'codex.history') AS source, item_at AS at, received_at
			FROM history_items
			WHERE agent_id=$1 AND session_id=$2 AND lower(trim(kind)) NOT IN ('usage','response.usage','token_usage','billing','cost')
		), remote AS (
			SELECT rm.id, rm.agent_id, r.session_id, rm.run_id, 0 AS item_index, rm.seq_start, rm.role, 'message' AS kind, COALESCE(rm.content, '') AS text, rm.payload, 'remote.run' AS source, rm.started_at AS at, rm.started_at AS received_at
			FROM run_messages rm
			JOIN runs r ON r.agent_id=rm.agent_id AND r.id=rm.run_id
			WHERE rm.agent_id=$1 AND r.session_id=$2
		)
		SELECT id, agent_id, session_id, run_id, item_index, seq_start, role, kind, text, payload, source, at, received_at
		FROM (
			SELECT * FROM history
			UNION ALL
			SELECT * FROM remote
		) items
		ORDER BY COALESCE(at, received_at) ASC, source ASC, item_index ASC, seq_start ASC
		LIMIT $3`, agentID, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []ConversationItem{}
	for rows.Next() {
		var item ConversationItem
		var payload []byte
		var at sql.NullTime
		var receivedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.SessionID, &item.RunID, &item.Index, &item.SeqStart, &item.Role, &item.Kind, &item.Text, &payload, &item.Source, &at, &receivedAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		if at.Valid {
			item.At = at.Time
		}
		if receivedAt.Valid {
			item.ReceivedAt = receivedAt.Time
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ListRunMessages(ctx context.Context, agentID, runID string) ([]RunMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, run_id, seq_start, COALESCE(seq_end, 0), role, COALESCE(content, ''), payload, status, started_at, finished_at FROM run_messages WHERE agent_id=$1 AND run_id=$2 ORDER BY seq_start ASC`, agentID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunMessage{}
	for rows.Next() {
		var item RunMessage
		var payload []byte
		var finished sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.RunID, &item.SeqStart, &item.SeqEnd, &item.Role, &item.Content, &payload, &item.Status, &item.StartedAt, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			item.FinishedAt = finished.Time
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListRunSteps(ctx context.Context, agentID, runID string) ([]RunStep, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, run_id, seq_start, COALESCE(seq_end, 0), kind, COALESCE(title, ''), status, payload, started_at, finished_at FROM run_steps WHERE agent_id=$1 AND run_id=$2 ORDER BY seq_start ASC`, agentID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RunStep{}
	for rows.Next() {
		var item RunStep
		var payload []byte
		var finished sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.RunID, &item.SeqStart, &item.SeqEnd, &item.Kind, &item.Title, &item.Status, &payload, &item.StartedAt, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			item.FinishedAt = finished.Time
		}
		if len(payload) > 0 {
			item.Payload = append(json.RawMessage(nil), payload...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListToolCalls(ctx context.Context, agentID, runID string) ([]ToolCall, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, agent_id, run_id, seq_start, COALESCE(seq_end, 0), COALESCE(tool_call_id, ''), COALESCE(tool_name, ''), arguments, output, status, COALESCE(approval_request_id, ''), started_at, finished_at FROM tool_calls WHERE agent_id=$1 AND run_id=$2 ORDER BY seq_start ASC`, agentID, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ToolCall{}
	for rows.Next() {
		var item ToolCall
		var arguments []byte
		var output []byte
		var finished sql.NullTime
		if err := rows.Scan(&item.ID, &item.AgentID, &item.RunID, &item.SeqStart, &item.SeqEnd, &item.ToolCallID, &item.ToolName, &arguments, &output, &item.Status, &item.ApprovalRequestID, &item.StartedAt, &finished); err != nil {
			return nil, err
		}
		if finished.Valid {
			item.FinishedAt = finished.Time
		}
		if len(arguments) > 0 {
			item.Arguments = append(json.RawMessage(nil), arguments...)
		}
		if len(output) > 0 {
			item.Output = append(json.RawMessage(nil), output...)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func projectionID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "prj_" + hex.EncodeToString(sum[:])[:32]
}

func payloadText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range []string{"text", "delta", "content", "message"} {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func payloadString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func payloadObject(raw json.RawMessage, keys ...string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	for _, key := range keys {
		if item, ok := value[key]; ok {
			return protocol.Raw(item)
		}
	}
	return nil
}

func eventTime(event protocol.RunEvent) time.Time {
	if event.At.IsZero() {
		return time.Now().UTC()
	}
	return event.At
}

func nullableTime(value time.Time, valid bool) any {
	if !valid {
		return nil
	}
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value
}

func stepTitle(event protocol.RunEvent) string {
	if title := payloadString(event.Payload, "title", "status", "name"); title != "" {
		return title
	}
	return event.Kind
}

func stepStatus(event protocol.RunEvent) string {
	if status := payloadString(event.Payload, "status"); status != "" {
		return status
	}
	return "done"
}

func toolStatus(kind string) string {
	switch kind {
	case "tool.call.started":
		return "running"
	case "tool.call.delta":
		return "running"
	case "tool.call.done":
		return "done"
	case "tool.output":
		return "output"
	case "terminal.output":
		return "output"
	case "approval.requested":
		return "awaiting_approval"
	default:
		return "running"
	}
}

func statusStepProjection(event protocol.RunEvent) (string, string, string) {
	if event.Kind != "run.status" {
		return "", "", ""
	}
	switch payloadString(event.Payload, "status") {
	case "codex.approval.unsupported":
		return "approval", "Approval unsupported", "unsupported"
	default:
		return "", "", ""
	}
}
