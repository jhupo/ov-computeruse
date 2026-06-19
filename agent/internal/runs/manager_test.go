package runs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/runtime"
)

type captureSink struct {
	events []protocol.RunEvent
}

func (s *captureSink) Emit(_ context.Context, event protocol.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}

type orderedRecorder struct {
	mu    sync.Mutex
	items []string
}

func (r *orderedRecorder) append(item string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, item)
}

func (r *orderedRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.items...)
}

type orderedSink struct {
	recorder *orderedRecorder
}

func (s orderedSink) Emit(_ context.Context, event protocol.RunEvent) error {
	s.recorder.append("event:" + event.Kind)
	return nil
}

type orderedAckStore struct {
	recorder *orderedRecorder
	mu       sync.Mutex
	acks     map[string]protocol.Ack
}

func newOrderedAckStore(recorder *orderedRecorder) *orderedAckStore {
	return &orderedAckStore{recorder: recorder, acks: map[string]protocol.Ack{}}
}

func (s *orderedAckStore) CommandAck(_ context.Context, commandID string) (protocol.Ack, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ack, ok := s.acks[commandID]
	return ack, ok, nil
}

func (s *orderedAckStore) SaveCommandAck(_ context.Context, ack protocol.Ack) error {
	s.recorder.append("ack:" + ack.Status)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acks[ack.CommandID] = ack
	return nil
}

func (s *orderedAckStore) LastRunEventSeq(context.Context) (uint64, error) {
	return 0, nil
}

func (s *orderedAckStore) ReconcileInterruptedRuns(context.Context) ([]protocol.RunEvent, error) {
	return nil, nil
}

type blockingRuntime struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingRuntime() *blockingRuntime {
	return &blockingRuntime{started: make(chan struct{}), release: make(chan struct{})}
}

func (r *blockingRuntime) Name() string {
	return "blocking"
}

func (r *blockingRuntime) NewSession(ctx context.Context, _ protocol.Command, _ runtime.Sink) error {
	return r.wait(ctx)
}

func (r *blockingRuntime) Resume(ctx context.Context, _ protocol.Command, _ runtime.Sink) error {
	return r.wait(ctx)
}

func (r *blockingRuntime) Send(ctx context.Context, _ protocol.Command, _ runtime.Sink) error {
	return r.wait(ctx)
}

func (r *blockingRuntime) Stop(context.Context, protocol.Command) error {
	return nil
}

func (r *blockingRuntime) wait(ctx context.Context) error {
	r.once.Do(func() { close(r.started) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.release:
		return nil
	}
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

func TestStartPersistsRunStartedBeforeAcceptedAck(t *testing.T) {
	recorder := &orderedRecorder{}
	rt := newBlockingRuntime()
	manager := NewManager(rt, orderedSink{recorder: recorder}, nil)
	manager.SetAckStore(newOrderedAckStore(recorder))

	ack := manager.Handle(context.Background(), protocol.Command{
		CommandID: "cmd_1",
		RunID:     "run_1",
		Kind:      "command.new_session",
		ProjectID: "project_1",
	})
	if ack.Status != "ok" {
		t.Fatalf("ack status = %q, want ok", ack.Status)
	}

	items := recorder.snapshot()
	if len(items) < 2 {
		t.Fatalf("recorded items = %v, want run.started before ack", items)
	}
	if items[0] != "event:run.started" || items[1] != "ack:ok" {
		t.Fatalf("recorded order = %v, want event:run.started then ack:ok", items)
	}

	duplicate := manager.Handle(context.Background(), protocol.Command{
		CommandID: "cmd_1",
		RunID:     "run_1",
		Kind:      "command.new_session",
		ProjectID: "project_1",
	})
	if duplicate.Status != ack.Status || duplicate.RunID != ack.RunID {
		t.Fatalf("duplicate ack = %+v, want replay of %+v", duplicate, ack)
	}

	select {
	case <-rt.started:
	case <-time.After(time.Second):
		t.Fatal("runtime did not start")
	}
	close(rt.release)
}
