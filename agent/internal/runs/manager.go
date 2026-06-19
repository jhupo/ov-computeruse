package runs

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/runtime"
)

var (
	ErrRunAlreadyActive     = errors.New("run already active")
	ErrSessionAlreadyActive = errors.New("session already has an active run")
	ErrRunNotFound          = errors.New("run not found")
	ErrRunIDRequired        = errors.New("run id required")
	ErrApprovalNotFound     = errors.New("approval request not found")
	ErrCommandDeadline      = errors.New("command deadline exceeded")
)

type State string

const (
	StateIdle             State = "idle"
	StateStarting         State = "starting"
	StateRunning          State = "running"
	StateAwaitingApproval State = "awaiting_approval"
	StateStopping         State = "stopping"
)

type EventSink interface {
	Emit(context.Context, protocol.RunEvent) error
}

type AckStore interface {
	CommandAck(context.Context, string) (protocol.Ack, bool, error)
	SaveCommandAck(context.Context, protocol.Ack) error
	LastRunEventSeq(context.Context) (uint64, error)
	ReconcileInterruptedRuns(context.Context) ([]protocol.RunEvent, error)
}

type Manager struct {
	mu        sync.Mutex
	runtime   runtime.Runtime
	sink      EventSink
	acks      AckStore
	logger    *slog.Logger
	active    map[string]*activeRun
	commands  map[string]protocol.Ack
	eventSeq  uint64
	maxActive int
}

type activeRun struct {
	command   protocol.Command
	cancel    context.CancelFunc
	state     State
	stopping  bool
	approvals map[string]chan protocol.ApprovalDecision
	sessions  map[string]struct{}
}

func sessionAliasSet(values ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out[value] = struct{}{}
		}
	}
	return out
}

func (r *activeRun) addSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	if r.sessions == nil {
		r.sessions = map[string]struct{}{}
	}
	r.sessions[sessionID] = struct{}{}
}

func (r *activeRun) hasSession(sessionID string) bool {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return false
	}
	if _, ok := r.sessions[sessionID]; ok {
		return true
	}
	return strings.TrimSpace(r.command.SessionID) == sessionID
}

func NewManager(rt runtime.Runtime, sink EventSink, logger *slog.Logger) *Manager {
	if rt == nil {
		rt = runtime.NewNoop()
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		runtime:   rt,
		sink:      sink,
		logger:    logger,
		active:    make(map[string]*activeRun),
		commands:  make(map[string]protocol.Ack),
		maxActive: 1,
	}
}

func (m *Manager) SetMaxActive(maxActive int) {
	if maxActive < 1 {
		maxActive = 1
	}
	m.mu.Lock()
	m.maxActive = maxActive
	m.mu.Unlock()
}

func (m *Manager) MaxActive() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxActive
}

func (m *Manager) RuntimeName() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.runtime == nil {
		return ""
	}
	return m.runtime.Name()
}

func (m *Manager) SetSink(sink EventSink) {
	m.mu.Lock()
	m.sink = sink
	m.mu.Unlock()
}

func (m *Manager) SetAckStore(store AckStore) {
	m.mu.Lock()
	m.acks = store
	m.mu.Unlock()
	if store == nil {
		return
	}
	if events, err := store.ReconcileInterruptedRuns(context.Background()); err == nil && len(events) > 0 {
		m.logger.Warn("interrupted runs reconciled", "count", len(events))
	} else if err != nil {
		m.logger.Warn("interrupted run reconciliation failed", "error", err)
	}
	m.mu.Lock()
	if store != nil {
		if seq, err := store.LastRunEventSeq(context.Background()); err == nil && seq > m.eventSeq {
			m.eventSeq = seq
		} else if err != nil {
			m.logger.Warn("run event sequence restore failed", "error", err)
		}
	}
	m.mu.Unlock()
}

func (m *Manager) Handle(ctx context.Context, command protocol.Command) protocol.Ack {
	if command.CommandID == "" {
		command.CommandID = protocol.NewID("cmd")
	}
	if ack, ok := m.cachedAck(command.CommandID); ok {
		return ack
	}
	if ack, ok := m.storedAck(ctx, command.CommandID); ok {
		return ack
	}

	switch command.Kind {
	case "command.new_session", "new_session", "command.resume", "resume", "command.send", "send":
		return m.start(ctx, command)
	case "command.stop", "stop":
		return m.stop(ctx, command)
	default:
		return m.remember(protocol.Ack{
			CommandID: command.CommandID,
			RunID:     command.RunID,
			Status:    "rejected",
			Message:   "unknown command kind",
			At:        time.Now().UTC(),
		})
	}
}

func (m *Manager) RunningRuns() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	runs := make([]string, 0, len(m.active))
	for runID := range m.active {
		runs = append(runs, runID)
	}
	return runs
}

func (m *Manager) LastEventSeq() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eventSeq
}

func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.active) == 0 {
		return StateIdle
	}
	for _, run := range m.active {
		return run.state
	}
	return StateIdle
}

func (m *Manager) Emit(ctx context.Context, event protocol.RunEvent) error {
	m.mu.Lock()
	m.applyRuntimeSessionUpdateLocked(event)
	m.eventSeq++
	event.Seq = m.eventSeq
	if event.EventID == "" {
		event.EventID = protocol.NewID("evt")
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	sink := m.sink
	m.mu.Unlock()
	if sink == nil {
		return nil
	}
	return sink.Emit(ctx, event)
}

func (m *Manager) applyRuntimeSessionUpdateLocked(event protocol.RunEvent) {
	if event.Kind != "session.updated" || len(event.Payload) == 0 {
		return
	}
	run := m.active[event.RunID]
	if run == nil {
		return
	}
	var session protocol.RuntimeSession
	if json.Unmarshal(event.Payload, &session) != nil {
		return
	}
	run.addSession(session.SessionID)
	run.addSession(session.NativeSessionID)
	if session.SessionID != "" && run.command.SessionID == "" {
		run.command.SessionID = session.SessionID
	}
	if session.ProjectID != "" && run.command.ProjectID == "" {
		run.command.ProjectID = session.ProjectID
	}
}

func (m *Manager) EmitStatus(ctx context.Context, trigger protocol.RunEvent, status string, payload map[string]any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["status"] = status
	return m.Emit(ctx, protocol.RunEvent{
		RunID:     trigger.RunID,
		CommandID: trigger.CommandID,
		ProjectID: trigger.ProjectID,
		SessionID: trigger.SessionID,
		Kind:      "run.status",
		Payload:   protocol.Raw(payload),
	})
}

func (m *Manager) start(ctx context.Context, command protocol.Command) protocol.Ack {
	if command.RunID == "" {
		command.RunID = protocol.NewID("run")
	}
	runCtx, cancel, err := commandContext(command)
	if err != nil {
		return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "rejected", Message: err.Error(), At: time.Now().UTC()})
	}

	m.mu.Lock()
	if len(m.active) >= m.maxActive {
		m.mu.Unlock()
		cancel()
		return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "rejected", Message: ErrRunAlreadyActive.Error(), At: time.Now().UTC()})
	}
	if _, exists := m.active[command.RunID]; exists {
		m.mu.Unlock()
		cancel()
		return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "duplicate", Message: "run already active", At: time.Now().UTC()})
	}
	if command.SessionID != "" {
		for _, run := range m.active {
			if run.hasSession(command.SessionID) {
				m.mu.Unlock()
				cancel()
				return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "rejected", Message: ErrSessionAlreadyActive.Error(), At: time.Now().UTC()})
			}
		}
	}
	m.active[command.RunID] = &activeRun{command: command, cancel: cancel, state: StateStarting, approvals: map[string]chan protocol.ApprovalDecision{}, sessions: sessionAliasSet(command.SessionID)}
	m.mu.Unlock()

	if err := m.emitRunStarted(ctx, command); err != nil {
		m.mu.Lock()
		delete(m.active, command.RunID)
		m.mu.Unlock()
		cancel()
		return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "rejected", Message: err.Error(), At: time.Now().UTC()})
	}
	ack := m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "ok", Message: "run accepted", At: time.Now().UTC()})
	go m.execute(runCtx, command)
	return ack
}

func (m *Manager) AwaitApproval(ctx context.Context, request protocol.ApprovalRequest) (protocol.ApprovalDecision, error) {
	if request.ID == "" {
		request.ID = protocol.NewID("apr")
	}
	if request.At.IsZero() {
		request.At = time.Now().UTC()
	}
	ch := make(chan protocol.ApprovalDecision, 1)
	m.mu.Lock()
	run := m.active[request.RunID]
	if run == nil {
		m.mu.Unlock()
		return protocol.ApprovalDecision{}, ErrRunNotFound
	}
	run.state = StateAwaitingApproval
	run.approvals[request.ID] = ch
	m.mu.Unlock()

	_ = m.Emit(ctx, protocol.RunEvent{
		RunID:     request.RunID,
		ProjectID: request.ProjectID,
		SessionID: request.SessionID,
		Kind:      "run.awaiting_approval",
		Payload:   protocol.Raw(map[string]string{"approval_id": request.ID}),
	})
	_ = m.Emit(ctx, protocol.RunEvent{
		RunID:     request.RunID,
		ProjectID: request.ProjectID,
		SessionID: request.SessionID,
		Kind:      "approval.requested",
		Payload:   protocol.Raw(request),
	})

	select {
	case decision := <-ch:
		m.mu.Lock()
		if run := m.active[request.RunID]; run != nil {
			delete(run.approvals, request.ID)
			if run.state == StateAwaitingApproval {
				run.state = StateRunning
			}
		}
		m.mu.Unlock()
		return decision, nil
	case <-ctx.Done():
		m.mu.Lock()
		if run := m.active[request.RunID]; run != nil {
			delete(run.approvals, request.ID)
			if run.state == StateAwaitingApproval {
				run.state = StateRunning
			}
		}
		m.mu.Unlock()
		return protocol.ApprovalDecision{}, ctx.Err()
	}
}

func (m *Manager) DecideApproval(ctx context.Context, decision protocol.ApprovalDecision) protocol.Ack {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, run := range m.active {
		ch := run.approvals[decision.ApprovalID]
		if ch == nil {
			continue
		}
		select {
		case ch <- decision:
		default:
		}
		return protocol.Ack{RunID: run.command.RunID, Status: "ok", Message: "approval decision accepted", At: time.Now().UTC()}
	}
	return protocol.Ack{Status: "rejected", Message: ErrApprovalNotFound.Error(), At: time.Now().UTC()}
}

func (m *Manager) stop(ctx context.Context, command protocol.Command) protocol.Ack {
	m.mu.Lock()
	run := m.active[command.RunID]
	if run == nil && command.SessionID != "" {
		for _, candidate := range m.active {
			if candidate.hasSession(command.SessionID) {
				run = candidate
				break
			}
		}
	}
	if run == nil {
		m.mu.Unlock()
		return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "rejected", Message: ErrRunNotFound.Error(), At: time.Now().UTC()})
	}
	run.state = StateStopping
	run.stopping = true
	m.mu.Unlock()

	_ = m.runtime.Stop(ctx, run.command)
	run.cancel()
	return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: run.command.RunID, Status: "ok", Message: "stop requested", At: time.Now().UTC()})
}

func (m *Manager) execute(ctx context.Context, command protocol.Command) {
	m.setState(command.RunID, StateRunning)
	if prompt := commandPrompt(command); prompt != "" {
		_ = m.Emit(ctx, protocol.RunEvent{
			RunID:     command.RunID,
			CommandID: command.CommandID,
			ProjectID: command.ProjectID,
			SessionID: command.SessionID,
			Kind:      "user.message",
			Payload:   protocol.Raw(map[string]string{"text": prompt}),
		})
	}

	var err error
	switch command.Kind {
	case "command.new_session", "new_session":
		err = m.runtime.NewSession(ctx, command, m)
	case "command.resume", "resume":
		err = m.runtime.Resume(ctx, command, m)
	default:
		err = m.runtime.Send(ctx, command, m)
	}

	kind := "run.done"
	payload := protocol.Raw(map[string]string{"status": "done"})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		kind = "run.error"
		payload = protocol.Raw(map[string]string{"error": ErrCommandDeadline.Error(), "status": "deadline_exceeded"})
	} else if errors.Is(ctx.Err(), context.Canceled) && m.wasStopping(command.RunID) {
		kind = "run.stopped"
		payload = protocol.Raw(map[string]string{"status": "stopped"})
	} else if errors.Is(ctx.Err(), context.Canceled) {
		kind = "run.error"
		payload = protocol.Raw(map[string]string{"error": context.Canceled.Error(), "status": "canceled"})
	} else if err != nil {
		kind = "run.error"
		payload = protocol.Raw(map[string]string{"error": err.Error()})
	}
	_ = m.Emit(context.Background(), protocol.RunEvent{
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      kind,
		Payload:   payload,
	})

	m.mu.Lock()
	delete(m.active, command.RunID)
	m.mu.Unlock()
}

func (m *Manager) emitRunStarted(ctx context.Context, command protocol.Command) error {
	return m.Emit(ctx, protocol.RunEvent{
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      "run.started",
	})
}

func commandPrompt(command protocol.Command) string {
	if len(command.Payload) == 0 {
		return ""
	}
	var payload struct {
		Prompt string `json:"prompt"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return ""
	}
	if payload.Prompt != "" {
		return payload.Prompt
	}
	return payload.Text
}

func commandContext(command protocol.Command) (context.Context, context.CancelFunc, error) {
	if command.DeadlineAt.IsZero() {
		ctx, cancel := context.WithCancel(context.Background())
		return ctx, cancel, nil
	}
	deadline := command.DeadlineAt.UTC()
	if !deadline.After(time.Now().UTC()) {
		return nil, nil, ErrCommandDeadline
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	return ctx, cancel, nil
}

func (m *Manager) wasStopping(runID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	run := m.active[runID]
	return run != nil && run.stopping
}

func (m *Manager) setState(runID string, state State) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if run := m.active[runID]; run != nil {
		run.state = state
	}
}

func (m *Manager) cachedAck(commandID string) (protocol.Ack, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ack, ok := m.commands[commandID]
	return ack, ok
}

func (m *Manager) storedAck(ctx context.Context, commandID string) (protocol.Ack, bool) {
	m.mu.Lock()
	store := m.acks
	m.mu.Unlock()
	if store == nil || commandID == "" {
		return protocol.Ack{}, false
	}
	ack, ok, err := store.CommandAck(ctx, commandID)
	if err != nil {
		m.logger.WarnContext(ctx, "command ack cache load failed", "command_id", commandID, "error", err)
		return protocol.Ack{}, false
	}
	if !ok {
		return protocol.Ack{}, false
	}
	m.mu.Lock()
	m.commands[commandID] = ack
	m.mu.Unlock()
	return ack, true
}

func (m *Manager) remember(ack protocol.Ack) protocol.Ack {
	m.mu.Lock()
	store := m.acks
	m.commands[ack.CommandID] = ack
	m.mu.Unlock()
	if store != nil && ack.CommandID != "" {
		if err := store.SaveCommandAck(context.Background(), ack); err != nil {
			m.logger.Warn("command ack cache save failed", "command_id", ack.CommandID, "error", err)
		}
	}
	return ack
}
