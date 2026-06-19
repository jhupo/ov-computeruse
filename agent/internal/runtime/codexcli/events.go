package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

type execEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Usage    json.RawMessage `json:"usage,omitempty"`
	Message  string          `json:"message,omitempty"`
	Error    execError       `json:"error,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

type execError struct {
	Message string `json:"message,omitempty"`
}

type execItem struct {
	ID                string          `json:"id,omitempty"`
	Type              string          `json:"type,omitempty"`
	Text              string          `json:"text,omitempty"`
	Summary           string          `json:"summary,omitempty"`
	Name              string          `json:"name,omitempty"`
	Server            string          `json:"server,omitempty"`
	Tool              string          `json:"tool,omitempty"`
	Query             string          `json:"query,omitempty"`
	Status            string          `json:"status,omitempty"`
	Command           string          `json:"command,omitempty"`
	AggregatedOutput  string          `json:"aggregated_output,omitempty"`
	Output            string          `json:"output,omitempty"`
	Action            json.RawMessage `json:"action,omitempty"`
	Prompt            string          `json:"prompt,omitempty"`
	SenderThreadID    string          `json:"sender_thread_id,omitempty"`
	ExitCode          *int            `json:"exit_code,omitempty"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
	Result            json.RawMessage `json:"result,omitempty"`
	Error             json.RawMessage `json:"error,omitempty"`
	Changes           json.RawMessage `json:"changes,omitempty"`
	Items             json.RawMessage `json:"items,omitempty"`
	ReceiverThreadIDs json.RawMessage `json:"receiver_thread_ids,omitempty"`
	AgentsStates      json.RawMessage `json:"agents_states,omitempty"`
	Raw               json.RawMessage `json:"-"`
}

type completionSignal struct {
	done atomic.Bool
}

func (s *completionSignal) MarkDone() {
	if s != nil {
		s.done.Store(true)
	}
}

func (s *completionSignal) Done() bool {
	return s != nil && s.done.Load()
}

func (a *Adapter) readStdout(ctx context.Context, stdout io.Reader, command protocol.Command, resolved localstate.CommandContext, sink agentruntime.Sink, completion *completionSignal) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	mapper := newEventMapper(a)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event := execEvent{Raw: append(json.RawMessage(nil), line...)}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			if emitErr := emit(ctx, sink, command, "terminal.output", map[string]string{"stream": "stdout", "text": line}); emitErr != nil {
				return emitErr
			}
			continue
		}
		if isTerminalExecEvent(event.Type) {
			completion.MarkDone()
		}
		if err := mapper.emitEvent(ctx, command, resolved, event, sink); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func isTerminalExecEvent(eventType string) bool {
	switch eventType {
	case "turn.completed", "turn.failed", "error":
		return true
	default:
		return false
	}
}

func readStderr(ctx context.Context, stderr io.Reader, command protocol.Command, sink agentruntime.Sink) error {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 16<<10), 2<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := emit(ctx, sink, command, "terminal.output", map[string]string{"stream": "stderr", "text": line}); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func decodeItem(raw json.RawMessage) execItem {
	item := execItem{Raw: append(json.RawMessage(nil), raw...)}
	_ = json.Unmarshal(raw, &item)
	return item
}

func rawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
