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
			dashRunSubscriptionKey("agent_1", "run_1"): {AgentID: "agent_1", RunID: "run_1", AfterSeq: 4},
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
			dashRunSubscriptionKey("agent_1", "run_1"): {AgentID: "agent_1", RunID: "run_1", AfterSeq: 5},
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

func TestDashAcceptsSubscribedSessionRunEventBroadcast(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSessionSubscriptionKey("agent_1", "session_1"): {AgentID: "agent_1", SessionID: "session_1"},
		},
	}
	event := protocol.RunEvent{RunID: "run_1", SessionID: "session_1", Seq: 1, Kind: "assistant.message.delta"}
	data := dashEvent("run.event", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, event)
	if !dashAcceptsBroadcast(dash, data) {
		t.Fatal("expected subscribed session dash to accept run event")
	}
}

func TestDashAcceptsSubscribedNativeSessionRunEventBroadcast(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSessionSubscriptionKey("agent_1", "native_thread_1"): {AgentID: "agent_1", SessionID: "native_thread_1"},
		},
	}
	event := protocol.RunEvent{
		RunID: "run_1",
		Seq:   1,
		Kind:  "assistant.message.delta",
		Payload: protocol.Raw(map[string]string{
			"runtime":   protocol.RuntimeCodexCLI,
			"thread_id": "native_thread_1",
			"text":      "hello",
		}),
	}
	data := dashEvent("run.event", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, event)
	if !dashAcceptsBroadcast(dash, data) {
		t.Fatal("expected native-thread session subscription to accept run event")
	}
}

func TestDashAdvancesRunSubscriptionCursorAfterBroadcast(t *testing.T) {
	key := dashRunSubscriptionKey("agent_1", "run_1")
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			key: {AgentID: "agent_1", RunID: "run_1", AfterSeq: 4},
		},
	}
	event := protocol.RunEvent{RunID: "run_1", Seq: 5, Kind: "assistant.message.delta"}
	data := dashEvent("run.event", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, event)
	if !dashAcceptsBroadcast(dash, data) {
		t.Fatal("expected subscribed dash to accept new run event")
	}
	if got := dash.Subscriptions[key].AfterSeq; got != 5 {
		t.Fatalf("after seq = %d, want 5", got)
	}
	if dashAcceptsBroadcast(dash, data) {
		t.Fatal("expected advanced cursor to reject repeated run event")
	}
}

func TestDashFiltersRuntimeTimelineUpdateBySessionSubscription(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSessionSubscriptionKey("agent_1", "session_1"): {AgentID: "agent_1", SessionID: "session_1"},
		},
	}
	accepted := dashEvent("runtime.timeline.updated", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, map[string]any{"session_id": "session_1"})
	if !dashAcceptsBroadcast(dash, accepted) {
		t.Fatal("expected subscribed session runtime timeline update")
	}
	rejected := dashEvent("runtime.timeline.updated", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, map[string]any{"session_id": "session_2"})
	if dashAcceptsBroadcast(dash, rejected) {
		t.Fatal("unexpected runtime timeline update for another session")
	}
}

func TestDashFiltersRuntimeTimelineUpdateByNativeSession(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSessionSubscriptionKey("agent_1", "native_thread_1"): {AgentID: "agent_1", SessionID: "native_thread_1"},
		},
	}
	accepted := dashEvent("runtime.timeline.updated", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, map[string]any{
		"session_id":        "session_1",
		"native_session_id": "native_thread_1",
	})
	if !dashAcceptsBroadcast(dash, accepted) {
		t.Fatal("expected subscribed native session runtime timeline update")
	}
}

func TestDashFiltersRuntimeSessionUpdateByNativeSession(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashSessionSubscriptionKey("agent_1", "native_thread_1"): {AgentID: "agent_1", SessionID: "native_thread_1"},
		},
	}
	accepted := dashEvent("runtime.session.updated", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, protocol.RuntimeSession{
		Runtime:         protocol.RuntimeCodexCLI,
		SessionID:       "session_1",
		NativeSessionID: "native_thread_1",
	})
	if !dashAcceptsBroadcast(dash, accepted) {
		t.Fatal("expected subscribed native session runtime session update")
	}
	rejected := dashEvent("runtime.session.updated", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, protocol.RuntimeSession{
		Runtime:         protocol.RuntimeCodexCLI,
		SessionID:       "session_2",
		NativeSessionID: "native_thread_2",
	})
	if dashAcceptsBroadcast(dash, rejected) {
		t.Fatal("unexpected runtime session update for another native session")
	}
}

func TestDashFiltersCommandAckByRunSubscription(t *testing.T) {
	dash := &DashConn{
		Subscriptions: map[string]DashSubscription{
			dashRunSubscriptionKey("agent_1", "run_1"): {AgentID: "agent_1", RunID: "run_1"},
		},
	}
	accepted := dashEvent("command.ack", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, protocol.Ack{RunID: "run_1", Status: "ok"})
	if !dashAcceptsBroadcast(dash, accepted) {
		t.Fatal("expected subscribed run command ack")
	}
	rejected := dashEvent("command.ack", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, protocol.Ack{RunID: "run_2", Status: "ok"})
	if dashAcceptsBroadcast(dash, rejected) {
		t.Fatal("unexpected command ack for another run")
	}
}

func TestHistoryItemsUpdateDrivesRuntimeTimelineBroadcast(t *testing.T) {
	history := historyItemsUpdate(protocol.HistoryItems{
		SessionID:  "session_1",
		Cursor:     "cursor_1",
		Reset:      true,
		UploadID:   "upload_1",
		BatchIndex: 1,
		BatchCount: 2,
		Final:      true,
		Items: []protocol.HistoryItem{
			{Kind: "message", Role: "user", Text: "hello"},
			{Kind: "usage", Text: "tokens"},
			{Kind: "tool.call", Text: "git status"},
		},
	})
	if history["count"] != 2 {
		t.Fatalf("history item count = %#v, want 2", history["count"])
	}
	timeline := runtimeTimelineUpdateFromHistory(history)
	if timeline["source"] != "history.items" || timeline["session_id"] != "session_1" || timeline["cursor"] != "cursor_1" || timeline["final"] != true {
		t.Fatalf("timeline update = %#v", timeline)
	}
	data := dashEvent("runtime.timeline.updated", &AgentConn{AgentID: "agent_1", DeviceID: "device_1"}, timeline)
	var wire struct {
		Type    string         `json:"type"`
		AgentID string         `json:"agent_id"`
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("decode dash event: %v", err)
	}
	if wire.Type != "runtime.timeline.updated" || wire.AgentID != "agent_1" || wire.Payload["source"] != "history.items" {
		t.Fatalf("wire event = %+v", wire)
	}
}

func TestRunEventAckCarriesFailureDetails(t *testing.T) {
	event := protocol.RunEvent{EventID: "evt_1", RunID: "run_1", Seq: 42, Kind: "run.done"}
	ack := runEventAck(event, "failed", "ownership mismatch")
	if ack.EventID != "evt_1" || ack.RunID != "run_1" || ack.AckSeq != 42 {
		t.Fatalf("ack target = %+v", ack)
	}
	if ack.Status != "failed" || ack.Message != "ownership mismatch" {
		t.Fatalf("ack status/message = %q/%q", ack.Status, ack.Message)
	}
	if ack.At.IsZero() {
		t.Fatal("ack time should be set")
	}
}
