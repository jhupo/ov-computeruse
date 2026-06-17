package runs

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"ov-computeruse/agent/internal/protocol"
	"ov-computeruse/agent/internal/runtime"
)

var (
	ErrRunAlreadyActive = errors.New("run already active")
	ErrRunNotFound      = errors.New("run not found")
	ErrRunIDRequired    = errors.New("run id required")
)

type State string

const (
	StateIdle     State = "idle"
	StateStarting State = "starting"
	StateRunning  State = "running"
	StateStopping State = "stopping"
)

type EventSink interface {
	Emit(context.Context, protocol.RunEvent) error
}

type Manager struct {
	mu        sync.Mutex
	runtime   runtime.Runtime
	sink      EventSink
	logger    *slog.Logger
	active    map[string]*activeRun
	commands  map[string]protocol.Ack
	eventSeq  uint64
	maxActive int
}

type activeRun struct {
	command protocol.Command
	cancel  context.CancelFunc
	state   State
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

func (m *Manager) SetSink(sink EventSink) {
	m.mu.Lock()
	m.sink = sink
	m.mu.Unlock()
}

func (m *Manager) Handle(ctx context.Context, command protocol.Command) protocol.Ack {
	if command.CommandID == "" {
		command.CommandID = protocol.NewID("cmd")
	}
	if ack, ok := m.cachedAck(command.CommandID); ok {
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

func (m *Manager) start(ctx context.Context, command protocol.Command) protocol.Ack {
	if command.RunID == "" {
		command.RunID = protocol.NewID("run")
	}
	runCtx, cancel := context.WithCancel(context.Background())

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
	m.active[command.RunID] = &activeRun{command: command, cancel: cancel, state: StateStarting}
	m.mu.Unlock()

	go m.execute(runCtx, command)
	return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: command.RunID, Status: "ok", Message: "run accepted", At: time.Now().UTC()})
}

func (m *Manager) stop(ctx context.Context, command protocol.Command) protocol.Ack {
	m.mu.Lock()
	run := m.active[command.RunID]
	if run == nil && command.SessionID != "" {
		for _, candidate := range m.active {
			if candidate.command.SessionID == command.SessionID {
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
	m.mu.Unlock()

	_ = m.runtime.Stop(ctx, command)
	run.cancel()
	return m.remember(protocol.Ack{CommandID: command.CommandID, RunID: run.command.RunID, Status: "ok", Message: "stop requested", At: time.Now().UTC()})
}

func (m *Manager) execute(ctx context.Context, command protocol.Command) {
	m.setState(command.RunID, StateRunning)
	_ = m.Emit(ctx, protocol.RunEvent{
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      "run.started",
	})

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
	if errors.Is(ctx.Err(), context.Canceled) {
		kind = "run.stopped"
		payload = protocol.Raw(map[string]string{"status": "stopped"})
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

func (m *Manager) remember(ack protocol.Ack) protocol.Ack {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.commands[ack.CommandID] = ack
	return ack
}
