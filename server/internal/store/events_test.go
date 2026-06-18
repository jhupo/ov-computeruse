package store

import (
	"encoding/json"
	"testing"

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
