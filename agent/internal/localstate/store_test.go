package localstate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/protocol"
)

func TestResolveCommandContext(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	root := t.TempDir()
	projectPath := filepath.Join(root, "repo")
	sessionPath := filepath.Join(root, "history.jsonl")

	_, err = state.SaveScanResult(context.Background(), codexscan.Result{
		Projects: []codexscan.Project{{
			ID:           "project_1",
			Name:         "repo",
			Path:         projectPath,
			LastActiveAt: time.Now().UTC(),
		}},
		Sessions: []codexscan.Session{{
			ID:        "session_1",
			ProjectID: "project_1",
			Path:      sessionPath,
			CWD:       projectPath,
			UpdatedAt: time.Now().UTC(),
		}},
	})
	if err != nil {
		t.Fatalf("save scan result: %v", err)
	}

	resolved, err := state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "session_1"})
	if err != nil {
		t.Fatalf("resolve command context: %v", err)
	}
	if resolved.Session.ID != "session_1" {
		t.Fatalf("session id = %q, want session_1", resolved.Session.ID)
	}
	if resolved.Project.ID != "project_1" {
		t.Fatalf("project id = %q, want project_1", resolved.Project.ID)
	}

	_, err = state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "session_1", ProjectID: "other_project"})
	if err == nil {
		t.Fatal("expected project/session mismatch error")
	}
}

func TestResolveCommandContextAcceptsRuntimeSession(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	root := t.TempDir()
	projectPath := filepath.Join(root, "repo")
	updatedAt := time.Now().UTC().Add(-time.Minute)

	_, err = state.SaveScanResult(context.Background(), codexscan.Result{
		Projects: []codexscan.Project{{
			ID:           "project_1",
			Name:         "repo",
			Path:         projectPath,
			LastActiveAt: updatedAt,
		}},
	})
	if err != nil {
		t.Fatalf("save scan result: %v", err)
	}
	err = state.SaveRuntimeSession(context.Background(), RuntimeSession{
		SessionID:       "resp_session",
		Runtime:         "openai.responses",
		ProjectID:       "project_1",
		NativeSessionID: "resp_session",
		LastResponseID:  "resp_latest",
		ResumeMode:      "previous_response_id",
		UpdatedAt:       updatedAt,
	})
	if err != nil {
		t.Fatalf("save runtime session: %v", err)
	}

	resolved, err := state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "resp_session"})
	if err != nil {
		t.Fatalf("resolve runtime command context: %v", err)
	}
	if resolved.Session.ID != "resp_session" || resolved.Session.IDSource != "runtime_session" {
		t.Fatalf("runtime session = %+v, want resp_session runtime_session", resolved.Session)
	}
	if resolved.Project.ID != "project_1" {
		t.Fatalf("project id = %q, want project_1", resolved.Project.ID)
	}

	_, err = state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "resp_session", ProjectID: "other_project"})
	if err == nil {
		t.Fatal("expected runtime project/session mismatch error")
	}
	byResponse, err := state.RuntimeSession(context.Background(), "resp_latest", "openai.responses")
	if err != nil {
		t.Fatalf("runtime session by response id: %v", err)
	}
	if byResponse.SessionID != "resp_session" || byResponse.LastResponseID != "resp_latest" {
		t.Fatalf("runtime session by response id = %+v", byResponse)
	}

	runtimeSessions, err := state.RuntimeSessions(context.Background())
	if err != nil {
		t.Fatalf("runtime sessions: %v", err)
	}
	if len(runtimeSessions) != 1 {
		t.Fatalf("runtime session count = %d, want 1", len(runtimeSessions))
	}
	if runtimeSessions[0].ProjectID != "project_1" || runtimeSessions[0].LastRunID != "" || runtimeSessions[0].UpdatedAt.IsZero() {
		t.Fatalf("runtime session not fully preserved: %+v", runtimeSessions[0])
	}
}

func TestSaveRunEventDoesNotProjectConflictingSeq(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	first := protocol.RunEvent{
		EventID: "evt_1",
		RunID:   "run_1",
		Seq:     1,
		Kind:    "assistant.message.delta",
		Payload: protocol.Raw(map[string]string{"role": "assistant", "text": "first"}),
		At:      time.Now().UTC(),
	}
	if err := state.SaveRunEvent(ctx, first); err != nil {
		t.Fatalf("save first event: %v", err)
	}
	conflict := protocol.RunEvent{
		EventID: "evt_2",
		RunID:   "run_1",
		Seq:     1,
		Kind:    "assistant.message.delta",
		Payload: protocol.Raw(map[string]string{"role": "assistant", "text": "conflict"}),
		At:      time.Now().UTC(),
	}
	if err := state.SaveRunEvent(ctx, conflict); err != nil {
		t.Fatalf("save conflicting event: %v", err)
	}
	rows, err := state.db.QueryContext(ctx, `SELECT event_id, payload FROM run_events WHERE run_id = ? AND seq = ?`, "run_1", 1)
	if err != nil {
		t.Fatalf("query run event: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var eventID string
		var payload []byte
		if err := rows.Scan(&eventID, &payload); err != nil {
			t.Fatalf("scan run event: %v", err)
		}
		if eventID != "evt_1" {
			t.Fatalf("event id = %q, want evt_1", eventID)
		}
		if string(payload) != string(first.Payload) {
			t.Fatalf("payload = %s, want %s", payload, first.Payload)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("run event count = %d, want 1", count)
	}
	var content string
	if err := state.db.QueryRowContext(ctx, `SELECT COALESCE(content, '') FROM run_messages WHERE run_id = ? AND seq_start = ?`, "run_1", 1).Scan(&content); err != nil {
		t.Fatalf("query projected message: %v", err)
	}
	if content != "first" {
		t.Fatalf("projected content = %q, want first", content)
	}
}

func TestMarkRunEventAckedPrefersEventID(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	event := protocol.RunEvent{
		EventID: "evt_acked",
		RunID:   "run_ack",
		Seq:     7,
		Kind:    "run.started",
		At:      time.Now().UTC(),
	}
	if err := state.SaveRunEvent(ctx, event); err != nil {
		t.Fatalf("save event: %v", err)
	}
	if err := state.MarkRunEventAcked(ctx, protocol.Ack{EventID: "evt_missing", RunID: "run_ack", AckSeq: 7, Status: "acked", At: time.Now().UTC()}); err != nil {
		t.Fatalf("ack missing event id: %v", err)
	}
	var ackedAt string
	err = state.db.QueryRowContext(ctx, `SELECT COALESCE(acked_at, '') FROM run_events WHERE event_id = ?`, "evt_acked").Scan(&ackedAt)
	if err != nil {
		t.Fatalf("query acked event: %v", err)
	}
	if ackedAt != "" {
		t.Fatal("event was acked through run_id+seq even though ack carried a different event_id")
	}
	if err := state.MarkRunEventAcked(ctx, protocol.Ack{EventID: "evt_acked", RunID: "run_ack", AckSeq: 7, Status: "acked", At: time.Now().UTC()}); err != nil {
		t.Fatalf("ack by event id: %v", err)
	}
	err = state.db.QueryRowContext(ctx, `SELECT COALESCE(acked_at, '') FROM run_events WHERE event_id = ?`, "evt_acked").Scan(&ackedAt)
	if err != nil {
		t.Fatalf("query acked event after event ack: %v", err)
	}
	if ackedAt == "" {
		t.Fatal("event was not acked by event_id")
	}
}

func TestReconcileInterruptedRunsCreatesPendingRunEvent(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	started := protocol.RunEvent{
		EventID:   "evt_started",
		RunID:     "run_interrupted",
		CommandID: "cmd_1",
		ProjectID: "project_1",
		SessionID: "session_1",
		Seq:       1,
		Kind:      "run.started",
		At:        time.Now().UTC().Add(-time.Minute),
	}
	if err := state.SaveRunEvent(ctx, started); err != nil {
		t.Fatalf("save started event: %v", err)
	}
	if err := state.MarkRunEventAcked(ctx, protocol.Ack{EventID: started.EventID, RunID: started.RunID, AckSeq: started.Seq, Status: "acked", At: time.Now().UTC()}); err != nil {
		t.Fatalf("ack started event: %v", err)
	}
	events, err := state.ReconcileInterruptedRuns(ctx)
	if err != nil {
		t.Fatalf("reconcile interrupted runs: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("interrupted event count = %d, want 1", len(events))
	}
	event := events[0]
	if event.RunID != started.RunID || event.CommandID != started.CommandID || event.Seq != 2 || event.Kind != "run.error" {
		t.Fatalf("interrupted event = %+v", event)
	}
	pending, err := state.PendingRunEvents(ctx, 10)
	if err != nil {
		t.Fatalf("pending events: %v", err)
	}
	if len(pending) != 1 || pending[0].EventID != event.EventID {
		t.Fatalf("pending events = %+v, want interrupted event", pending)
	}
	var status string
	if err := state.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE id = ?`, started.RunID).Scan(&status); err != nil {
		t.Fatalf("query run status: %v", err)
	}
	if status != "interrupted" {
		t.Fatalf("run status = %q, want interrupted", status)
	}
}
