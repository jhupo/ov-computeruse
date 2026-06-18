package app

import (
	"encoding/json"
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

func TestDashAcceptsSubscribedRunEventBroadcast(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSubscriptionKey("agent_1", "run_1"): {AgentID: "agent_1", RunID: "run_1", AfterSeq: 4},
		},
	}
	event := protocol.RunEvent{RunID: "run_1", Seq: 5, Kind: "assistant.message.done"}
	data := dashEvent("run.event", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, event)
	if !dashAcceptsBroadcast(dash, data) {
		t.Fatal("expected subscribed dash to accept new run event")
	}
}

func TestDashRejectsUnsubscribedOrOldRunEventBroadcast(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSubscriptionKey("agent_1", "run_1"): {AgentID: "agent_1", RunID: "run_1", AfterSeq: 5},
		},
	}
	cases := []protocol.RunEvent{
		{RunID: "run_1", Seq: 5, Kind: "assistant.message.done"},
		{RunID: "run_2", Seq: 6, Kind: "assistant.message.done"},
	}
	for _, event := range cases {
		data := dashEvent("run.event", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, event)
		if dashAcceptsBroadcast(dash, data) {
			t.Fatalf("unexpected broadcast accepted for event %+v", event)
		}
	}
}

func TestDashEventWrapsRunEventPayloadForSubscriptionFilter(t *testing.T) {
	event := protocol.RunEvent{RunID: "run_1", Seq: 3, Kind: "run.status"}
	data := dashEvent("run.event", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, event)
	var wire struct {
		Type    string            `json:"type"`
		AgentID string            `json:"agent_id"`
		Payload protocol.RunEvent `json:"payload"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("decode dash event: %v", err)
	}
	if wire.Type != "run.event" || wire.AgentID != "agent_1" || wire.Payload.RunID != "run_1" || wire.Payload.Seq != 3 {
		t.Fatalf("wire event = %+v", wire)
	}
}
