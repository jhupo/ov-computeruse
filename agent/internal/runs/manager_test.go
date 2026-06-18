package runs

import (
	"context"
	"encoding/json"
	"testing"

	"ov-computeruse/agent/internal/protocol"
)

type captureSink struct {
	events []protocol.RunEvent
}

func (s *captureSink) Emit(_ context.Context, event protocol.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestEmitStatusUsesManagerSequence(t *testing.T) {
	sink := &captureSink{}
	manager := NewManager(nil, sink, nil)
	trigger := protocol.RunEvent{
		RunID:     "run_1",
		CommandID: "cmd_1",
		ProjectID: "project_1",
		SessionID: "session_1",
		Seq:       99,
		Kind:      "run.done",
	}
	if err := manager.EmitStatus(context.Background(), trigger, "index.refresh.done", map[string]any{"duration_millis": 12}); err != nil {
		t.Fatalf("emit status: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(sink.events))
	}
	event := sink.events[0]
	if event.Seq != 1 {
		t.Fatalf("seq = %d, want manager assigned seq 1", event.Seq)
	}
	if event.RunID != trigger.RunID || event.CommandID != trigger.CommandID || event.ProjectID != trigger.ProjectID || event.SessionID != trigger.SessionID {
		t.Fatalf("event identity = %+v, trigger = %+v", event, trigger)
	}
	if event.Kind != "run.status" {
		t.Fatalf("kind = %q, want run.status", event.Kind)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["status"] != "index.refresh.done" {
		t.Fatalf("status = %v, want index.refresh.done", payload["status"])
	}
	if payload["duration_millis"] != float64(12) {
		t.Fatalf("duration = %v, want 12", payload["duration_millis"])
	}
}
