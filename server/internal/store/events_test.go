package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ov-computeruse/server/internal/protocol"
)

func TestRuntimeSessionFromEventOnlyAcceptsExplicitCodexSessions(t *testing.T) {
	event := protocol.RunEvent{
		Kind:      "session.updated",
		RunID:     "run_1",
		ProjectID: "project_1",
		SessionID: "session_1",
		Payload: protocol.Raw(protocol.RuntimeSession{
			Runtime:         protocol.RuntimeCodexCLI,
			NativeSessionID: "native_1",
		}),
	}
	runtimeSession, ok := runtimeSessionFromEvent(event)
	if !ok {
		t.Fatal("expected codex runtime session")
	}
	if runtimeSession.ProjectID != "project_1" || runtimeSession.SessionID != "session_1" || runtimeSession.LastRunID != "run_1" {
		t.Fatalf("runtime session not enriched from event: %+v", runtimeSession)
	}
}

func TestRuntimeSessionFromEventRejectsRunStatusAndOtherRuntimes(t *testing.T) {
	cases := []protocol.RunEvent{
		{
			Kind:    "run.status",
			Payload: protocol.Raw(protocol.RuntimeSession{Runtime: protocol.RuntimeCodexCLI, SessionID: "session_1"}),
		},
		{
			Kind:    "session.updated",
			Payload: protocol.Raw(protocol.RuntimeSession{Runtime: "other.agent", SessionID: "session_1"}),
		},
		{
			Kind:    "session.updated",
			Payload: protocol.Raw(protocol.RuntimeSession{Runtime: protocol.RuntimeCodexCLI}),
		},
	}
	for _, event := range cases {
		if runtimeSession, ok := runtimeSessionFromEvent(event); ok {
			t.Fatalf("unexpected runtime session from %+v: %+v", event, runtimeSession)
		}
	}
}

func TestTerminalOutputPayloadCanProjectAsToolOutput(t *testing.T) {
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
		"stderr":       "complete stderr",
	})
	if !shouldProjectTerminalOutputAsToolCall(full) {
		t.Fatal("complete terminal output should project onto tool output")
	}
}

func TestHeartbeatMissingRunCanBecomeStale(t *testing.T) {
	staleStatuses := []string{"running", "awaiting_approval", "stopping", " STOPPING "}
	for _, status := range staleStatuses {
		if !heartbeatMissingRunCanBecomeStale(status) {
			t.Fatalf("expected %q to become stale when missing from heartbeat", status)
		}
	}

	for _, status := range []string{"queued", "accepted", "stale", "stopped", "failed", ""} {
		if heartbeatMissingRunCanBecomeStale(status) {
			t.Fatalf("expected %q to remain unchanged when missing from heartbeat", status)
		}
	}
}

func TestHeartbeatReconciliationDoesNotAdvanceRunEventSeq(t *testing.T) {
	for _, query := range []string{heartbeatRunningRunUpdateSQL, heartbeatStaleRunUpdateSQL} {
		normalized := strings.ToLower(query)
		if strings.Contains(normalized, "last_event_seq") {
			t.Fatalf("heartbeat reconciliation must not update per-run event seq: %s", query)
		}
		if strings.Contains(normalized, "$4") {
			t.Fatalf("heartbeat reconciliation should not bind heartbeat global seq: %s", query)
		}
	}
}

func TestHeartbeatStaleCandidatesRequireGraceWindow(t *testing.T) {
	query := strings.ToLower(heartbeatStaleCandidateSQL)
	for _, want := range []string{"coalesce(last_event_at, started_at)", "<= $2"} {
		if !strings.Contains(query, want) {
			t.Fatalf("heartbeat stale candidate query missing %q: %s", want, heartbeatStaleCandidateSQL)
		}
	}
	if heartbeatRunStaleGrace < time.Minute {
		t.Fatalf("heartbeat stale grace = %s, want at least one minute", heartbeatRunStaleGrace)
	}
}

func TestStoreCommandCreatesRun(t *testing.T) {
	for _, kind := range []string{"command.new_session", "new_session", "command.resume", "resume", "command.send", "send"} {
		if !storeCommandCreatesRun(kind) {
			t.Fatalf("expected %q to create a run", kind)
		}
	}

	for _, kind := range []string{"command.stop", "stop", "command.approval_decision", "workspace.request"} {
		if storeCommandCreatesRun(kind) {
			t.Fatalf("expected %q to not create a run", kind)
		}
	}
}

func TestCommandMatchesIdempotencyAcceptsEquivalentCommand(t *testing.T) {
	existing := CommandRecord{
		RunID:     "run_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		Kind:      "command.send",
		Payload:   json.RawMessage(`{"prompt":"hi"}`),
	}
	incoming := protocol.Command{
		RunID:     "run_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		Kind:      "command.send",
		Payload:   json.RawMessage(`{"prompt":"hi"}`),
	}
	if !commandMatchesIdempotency(existing, incoming) {
		t.Fatal("expected equivalent command to reuse idempotency key")
	}
}

func TestCommandMatchesIdempotencyRejectsDifferentPayload(t *testing.T) {
	existing := CommandRecord{
		RunID:     "run_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		Kind:      "command.send",
		Payload:   json.RawMessage(`{"prompt":"hi"}`),
	}
	incoming := protocol.Command{
		RunID:     "run_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		Kind:      "command.send",
		Payload:   json.RawMessage(`{"prompt":"different"}`),
	}
	if commandMatchesIdempotency(existing, incoming) {
		t.Fatal("expected different payload to conflict")
	}
}

func TestCommandMatchesIdempotencyRejectsDifferentTarget(t *testing.T) {
	existing := CommandRecord{
		RunID:     "run_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		Kind:      "command.send",
		Payload:   json.RawMessage(`{"prompt":"hi"}`),
	}
	incoming := protocol.Command{
		RunID:     "run_1",
		SessionID: "session_2",
		ProjectID: "project_1",
		Kind:      "command.send",
		Payload:   json.RawMessage(`{"prompt":"hi"}`),
	}
	if commandMatchesIdempotency(existing, incoming) {
		t.Fatal("expected different target to conflict")
	}
}

func TestNormalizeCommandDoesNotAddDefaultExecutionDeadline(t *testing.T) {
	command := normalizeCommand(protocol.Command{Kind: "command.send"})
	if !command.DeadlineAt.IsZero() {
		t.Fatalf("deadline_at = %v, want zero for long-running Codex CLI execution", command.DeadlineAt)
	}
	if command.ExpiresAt.IsZero() {
		t.Fatal("expires_at should still be set to expire undispatched queued commands")
	}
	if time.Until(command.ExpiresAt) <= 0 {
		t.Fatalf("expires_at = %v, want future time", command.ExpiresAt)
	}
}

func TestNormalizeCommandPreservesExplicitExecutionDeadline(t *testing.T) {
	deadline := time.Now().UTC().Add(2 * time.Hour)
	command := normalizeCommand(protocol.Command{Kind: "command.send", DeadlineAt: deadline})
	if !command.DeadlineAt.Equal(deadline) {
		t.Fatalf("deadline_at = %v, want explicit deadline %v", command.DeadlineAt, deadline)
	}
}
