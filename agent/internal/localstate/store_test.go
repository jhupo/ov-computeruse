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
		SessionID:       "codex_session",
		Runtime:         protocol.RuntimeCodexCLI,
		ProjectID:       "project_1",
		NativeSessionID: "codex_native",
		ResumeMode:      "codex_cli_exec",
		LastRunID:       "run_1",
		Title:           "Fix bug",
		CWD:             projectPath,
		Model:           "gpt-5.1-codex-max",
		Profile:         "work",
		ApprovalPolicy:  "never",
		SandboxMode:     "read-only",
		ReasoningEffort: "high",
		LastTurnID:      "turn_1",
		LastItemIndex:   7,
		UpdatedAt:       updatedAt,
	})
	if err != nil {
		t.Fatalf("save runtime session: %v", err)
	}

	resolved, err := state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "codex_session"})
	if err != nil {
		t.Fatalf("resolve runtime command context: %v", err)
	}
	if resolved.Session.ID != "codex_session" || resolved.Session.IDSource != "runtime_session" {
		t.Fatalf("runtime session = %+v, want codex_session runtime_session", resolved.Session)
	}
	if resolved.Session.Title != "Fix bug" || resolved.Session.CWD != projectPath {
		t.Fatalf("runtime session context was not resolved: %+v", resolved.Session)
	}
	if resolved.Project.ID != "project_1" {
		t.Fatalf("project id = %q, want project_1", resolved.Project.ID)
	}

	_, err = state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "codex_session", ProjectID: "other_project"})
	if err == nil {
		t.Fatal("expected runtime project/session mismatch error")
	}
	byNative, err := state.RuntimeSession(context.Background(), "codex_native", protocol.RuntimeCodexCLI)
	if err != nil {
		t.Fatalf("runtime session by native id: %v", err)
	}
	if byNative.SessionID != "codex_session" || byNative.NativeSessionID != "codex_native" {
		t.Fatalf("runtime session by native id = %+v", byNative)
	}
	if byNative.Model != "gpt-5.1-codex-max" || byNative.Profile != "work" || byNative.ApprovalPolicy != "never" || byNative.SandboxMode != "read-only" || byNative.ReasoningEffort != "high" || byNative.LastTurnID != "turn_1" || byNative.LastItemIndex != 7 {
		t.Fatalf("runtime session metadata not preserved: %+v", byNative)
	}
	byRun, err := state.RuntimeSessionByRun(context.Background(), "run_1", protocol.RuntimeCodexCLI)
	if err != nil {
		t.Fatalf("runtime session by run id: %v", err)
	}
	if byRun.SessionID != "codex_session" || byRun.NativeSessionID != "codex_native" {
		t.Fatalf("runtime session by run id = %+v", byRun)
	}

	runtimeSessions, err := state.RuntimeSessions(context.Background())
	if err != nil {
		t.Fatalf("runtime sessions: %v", err)
	}
	if len(runtimeSessions) != 1 {
		t.Fatalf("runtime session count = %d, want 1", len(runtimeSessions))
	}
	if runtimeSessions[0].ProjectID != "project_1" || runtimeSessions[0].LastRunID != "run_1" || runtimeSessions[0].UpdatedAt.IsZero() {
		t.Fatalf("runtime session not fully preserved: %+v", runtimeSessions[0])
	}
}

func TestRuntimeSessionScanMergesLiveNativeSession(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	ctx := context.Background()
	liveAt := time.Now().UTC().Add(-time.Minute)
	if err := state.SaveRuntimeSession(ctx, RuntimeSession{
		SessionID:       "native_thread",
		Runtime:         protocol.RuntimeCodexCLI,
		ProjectID:       "project_1",
		NativeSessionID: "native_thread",
		ResumeMode:      "codex_cli_exec",
		LastRunID:       "run_1",
		UpdatedAt:       liveAt,
	}); err != nil {
		t.Fatalf("save live runtime session: %v", err)
	}

	_, err = state.SaveScanResult(ctx, codexscan.Result{
		Sessions: []codexscan.Session{{
			ID:        "history_session",
			ProjectID: "project_1",
			Path:      filepath.Join(t.TempDir(), "session.jsonl"),
			UpdatedAt: time.Now().UTC(),
		}},
		RuntimeSessions: []codexscan.RuntimeSession{{
			Runtime:         protocol.RuntimeCodexCLI,
			ProjectID:       "project_1",
			SessionID:       "history_session",
			NativeSessionID: "native_thread",
			ResumeMode:      "codex_cli_history_index",
			LastItemIndex:   12,
			UpdatedAt:       time.Now().UTC(),
		}},
	})
	if err != nil {
		t.Fatalf("save scanned runtime session: %v", err)
	}

	byRun, err := state.RuntimeSessionByRun(ctx, "run_1", protocol.RuntimeCodexCLI)
	if err != nil {
		t.Fatalf("runtime session by run: %v", err)
	}
	if byRun.SessionID != "history_session" || byRun.NativeSessionID != "native_thread" {
		t.Fatalf("runtime session by run = %+v", byRun)
	}
	if byRun.LastRunID != "run_1" {
		t.Fatalf("last run id = %q, want run_1", byRun.LastRunID)
	}

	byNative, err := state.RuntimeSession(ctx, "native_thread", protocol.RuntimeCodexCLI)
	if err != nil {
		t.Fatalf("runtime session by native: %v", err)
	}
	if byNative.SessionID != "history_session" {
		t.Fatalf("runtime session by native = %+v", byNative)
	}
	if byNative.LastItemIndex != 12 {
		t.Fatalf("last item index = %d, want 12", byNative.LastItemIndex)
	}

	sessions, err := state.RuntimeSessions(ctx)
	if err != nil {
		t.Fatalf("runtime sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("runtime session count = %d, want 1: %+v", len(sessions), sessions)
	}

	if err := state.SaveRuntimeSession(ctx, RuntimeSession{
		SessionID:       "native_thread",
		Runtime:         protocol.RuntimeCodexCLI,
		ProjectID:       "project_1",
		NativeSessionID: "native_thread",
		ResumeMode:      "codex_cli_exec",
		LastRunID:       "run_2",
		UpdatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("save native live runtime session after scan: %v", err)
	}
	byRun, err = state.RuntimeSessionByRun(ctx, "run_2", protocol.RuntimeCodexCLI)
	if err != nil {
		t.Fatalf("runtime session by second run: %v", err)
	}
	if byRun.SessionID != "history_session" || byRun.NativeSessionID != "native_thread" {
		t.Fatalf("runtime session regressed after live update: %+v", byRun)
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

func TestTerminalOutputPayloadProjectsAsToolOutput(t *testing.T) {
	raw := protocol.Raw(map[string]string{
		"tool_call_id": "tool_1",
		"text":         "streamed output",
	})
	if got := payloadString(raw, "tool_call_id"); got != "tool_1" {
		t.Fatalf("tool_call_id = %q, want tool_1", got)
	}
	if got := string(payloadObject(raw, "output", "result", "text")); got != `"streamed output"` {
		t.Fatalf("projected output = %s", got)
	}
	if got := toolStatus("terminal.output"); got != "output" {
		t.Fatalf("terminal output status = %q, want output", got)
	}
}

func TestTerminalOutputDeltaDoesNotOverwriteToolOutput(t *testing.T) {
	delta := protocol.Raw(map[string]string{
		"tool_call_id": "tool_1",
		"text":         "delta only",
	})
	if shouldProjectTerminalOutputAsToolCall(delta) {
		t.Fatal("streaming terminal delta should not overwrite projected tool output")
	}
	full := protocol.Raw(map[string]string{
		"tool_call_id": "tool_1",
		"stdout":       "complete stdout",
	})
	if !shouldProjectTerminalOutputAsToolCall(full) {
		t.Fatal("complete terminal output should project onto tool output")
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

func TestMarkRunEventAckErrorRecordsServerFailure(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	event := protocol.RunEvent{
		EventID: "evt_failed_ack",
		RunID:   "run_ack",
		Seq:     8,
		Kind:    "assistant.message.delta",
		At:      time.Now().UTC(),
	}
	if err := state.SaveRunEvent(ctx, event); err != nil {
		t.Fatalf("save event: %v", err)
	}
	if err := state.MarkRunEventAckError(ctx, protocol.Ack{EventID: "evt_missing", RunID: "run_ack", AckSeq: 8, Status: "failed", Message: "database unavailable", At: time.Now().UTC()}); err != nil {
		t.Fatalf("record missing event ack error: %v", err)
	}
	var lastError string
	err = state.db.QueryRowContext(ctx, `SELECT COALESCE(last_error, '') FROM run_events WHERE event_id = ?`, event.EventID).Scan(&lastError)
	if err != nil {
		t.Fatalf("query event error: %v", err)
	}
	if lastError != "" {
		t.Fatal("event error was recorded through run_id+seq even though ack carried a different event_id")
	}
	if err := state.MarkRunEventAckError(ctx, protocol.Ack{RunID: "run_ack", AckSeq: 8, Status: "failed", Message: "database unavailable", At: time.Now().UTC()}); err != nil {
		t.Fatalf("record ack error: %v", err)
	}
	err = state.db.QueryRowContext(ctx, `SELECT COALESCE(last_error, '') FROM run_events WHERE event_id = ?`, event.EventID).Scan(&lastError)
	if err != nil {
		t.Fatalf("query event error after ack: %v", err)
	}
	if lastError != "run event ack failed: database unavailable" {
		t.Fatalf("last_error = %q", lastError)
	}
}

func TestMarkRunEventSentReportsFirstSend(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	event := protocol.RunEvent{
		EventID: "evt_sent_once",
		RunID:   "run_1",
		Seq:     1,
		Kind:    "run.done",
		At:      time.Now().UTC(),
	}
	if err := state.SaveRunEvent(ctx, event); err != nil {
		t.Fatalf("save run event: %v", err)
	}
	first, err := state.MarkRunEventSent(ctx, event)
	if err != nil {
		t.Fatalf("mark first sent: %v", err)
	}
	if !first {
		t.Fatal("first send should be reported as first")
	}
	second, err := state.MarkRunEventSent(ctx, event)
	if err != nil {
		t.Fatalf("mark second sent: %v", err)
	}
	if second {
		t.Fatal("second send should be reported as retry")
	}
}

func TestCodexHistorySyncMarker(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	ctx := context.Background()
	synced, err := state.CodexHistorySynced(ctx, "evt_done")
	if err != nil {
		t.Fatalf("check missing sync marker: %v", err)
	}
	if synced {
		t.Fatal("missing sync marker reported as synced")
	}
	if err := state.MarkCodexHistorySynced(ctx, "evt_done", "session_1"); err != nil {
		t.Fatalf("mark history synced: %v", err)
	}
	synced, err = state.CodexHistorySynced(ctx, "evt_done")
	if err != nil {
		t.Fatalf("check sync marker: %v", err)
	}
	if !synced {
		t.Fatal("sync marker was not found")
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
