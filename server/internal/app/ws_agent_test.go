package app

import (
	"testing"

	"ov-computeruse/server/internal/protocol"
)

func TestRuntimeSessionFromRunEventOnlyAcceptsExplicitCodexSessions(t *testing.T) {
	event := protocol.RunEvent{
		Kind: "session.updated",
		Payload: protocol.Raw(protocol.RuntimeSession{
			Runtime:         protocol.RuntimeCodexCLI,
			SessionID:       "session_1",
			NativeSessionID: "native_1",
		}),
	}
	runtimeSession, ok := runtimeSessionFromRunEvent(event)
	if !ok {
		t.Fatal("expected runtime session")
	}
	if runtimeSession.Runtime != protocol.RuntimeCodexCLI || runtimeSession.NativeSessionID != "native_1" {
		t.Fatalf("runtime session = %+v", runtimeSession)
	}
}

func TestRuntimeSessionFromRunEventRejectsRunEventsAndOtherRuntimes(t *testing.T) {
	cases := []protocol.RunEvent{
		{
			Kind:    "run.done",
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
		if runtimeSession, ok := runtimeSessionFromRunEvent(event); ok {
			t.Fatalf("unexpected runtime session from %+v: %+v", event, runtimeSession)
		}
	}
}
