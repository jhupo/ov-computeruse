package store

import (
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
