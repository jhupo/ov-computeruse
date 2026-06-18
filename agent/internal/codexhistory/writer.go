package codexhistory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
)

type State interface {
	CodexRoot(context.Context) (string, error)
	ProjectPath(context.Context, string) (string, error)
	SessionPath(context.Context, string) (string, error)
	SaveSessionRecord(context.Context, codexscan.Session) error
	CodexHistorySynced(context.Context, string) (bool, error)
	MarkCodexHistorySynced(context.Context, string, string) error
}

type Writer struct {
	state State
}

func New(state State) Writer {
	return Writer{state: state}
}

func (w Writer) SyncRunEvent(ctx context.Context, event protocol.RunEvent) error {
	if w.state == nil || strings.TrimSpace(event.EventID) == "" || strings.TrimSpace(event.SessionID) == "" {
		return nil
	}
	if !syncableKind(event.Kind) {
		return nil
	}
	synced, err := w.state.CodexHistorySynced(ctx, event.EventID)
	if err != nil {
		return err
	}
	if synced {
		return nil
	}
	path, err := w.state.SessionPath(ctx, event.SessionID)
	if errors.Is(err, sql.ErrNoRows) {
		path, err = w.createSession(ctx, event)
	}
	if err != nil {
		return err
	}
	row, ok := rowFromEvent(event)
	if !ok {
		return nil
	}
	if err := appendJSONL(path, row); err != nil {
		return err
	}
	return w.state.MarkCodexHistorySynced(ctx, event.EventID, event.SessionID)
}

func (w Writer) createSession(ctx context.Context, event protocol.RunEvent) (string, error) {
	root, err := w.state.CodexRoot(ctx)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, "sessions", event.SessionID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	cwd, err := w.projectPath(ctx, event.ProjectID)
	if err != nil {
		return "", err
	}
	meta := map[string]any{
		"timestamp": eventTime(event).Format(time.RFC3339Nano),
		"type":      "session_meta",
		"payload": map[string]any{
			"id":  event.SessionID,
			"cwd": cwd,
		},
	}
	if err := appendJSONL(path, meta); err != nil {
		return "", err
	}
	if err := w.state.SaveSessionRecord(ctx, codexscan.Session{
		ID:        event.SessionID,
		IDSource:  "codex_session_meta",
		ProjectID: event.ProjectID,
		Path:      path,
		CWD:       cwd,
		UpdatedAt: eventTime(event),
	}); err != nil {
		return "", err
	}
	return path, nil
}

func (w Writer) projectPath(ctx context.Context, projectID string) (string, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return "", nil
	}
	path, err := w.state.ProjectPath(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

func syncableKind(kind string) bool {
	switch kind {
	case "user.message", "assistant.message.done", "tool.call.done", "tool.output", "approval.requested":
		return true
	default:
		return false
	}
}

func rowFromEvent(event protocol.RunEvent) (map[string]any, bool) {
	payload := map[string]any{}
	_ = json.Unmarshal(event.Payload, &payload)
	at := event.At
	at = eventTime(event)
	row := map[string]any{
		"timestamp": at.UTC().Format(time.RFC3339Nano),
		"type":      "response_item",
	}
	switch event.Kind {
	case "user.message":
		row["payload"] = messagePayload("user", payloadText(payload))
	case "assistant.message.done":
		row["payload"] = messagePayload("assistant", payloadText(payload))
	case "tool.call.done":
		row["payload"] = toolPayload(payload, "function_call")
	case "tool.output":
		row["payload"] = toolPayload(payload, "function_call_output")
	case "approval.requested":
		row["payload"] = approvalPayload(payload)
	default:
		return nil, false
	}
	return row, true
}

func eventTime(event protocol.RunEvent) time.Time {
	if event.At.IsZero() {
		return time.Now().UTC()
	}
	return event.At.UTC()
}

func messagePayload(role, text string) map[string]any {
	return map[string]any{
		"type": "message",
		"role": role,
		"content": []map[string]any{{
			"type": "text",
			"text": text,
		}},
	}
}

func toolPayload(payload map[string]any, payloadType string) map[string]any {
	out := map[string]any{"type": payloadType}
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func approvalPayload(payload map[string]any) map[string]any {
	out := map[string]any{"type": "mcp_approval_request"}
	for key, value := range payload {
		out[key] = value
	}
	return out
}

func payloadText(payload map[string]any) string {
	for _, key := range []string{"text", "message", "content", "delta"} {
		if text, ok := payload[key].(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func appendJSONL(path string, row map[string]any) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

var _ State = (*localstate.Store)(nil)
