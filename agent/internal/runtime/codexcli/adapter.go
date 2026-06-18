package codexcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

const runtimeName = protocol.RuntimeCodexCLI

type Config struct {
	BinPath string
	Model   string
	Profile string
	State   *localstate.Store
}

type Adapter struct {
	cfg    Config
	active activeRuns
}

type activeRuns struct {
	mu        sync.Mutex
	cancelBy  map[string]context.CancelFunc
	sessionBy map[string]string
}

type commandPayload struct {
	Prompt string `json:"prompt"`
	Text   string `json:"text"`
}

type execEvent struct {
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id,omitempty"`
	Item     json.RawMessage `json:"item,omitempty"`
	Usage    json.RawMessage `json:"usage,omitempty"`
	Message  string          `json:"message,omitempty"`
	Error    string          `json:"error,omitempty"`
	Raw      json.RawMessage `json:"-"`
}

type execItem struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type,omitempty"`
	Text    string          `json:"text,omitempty"`
	Name    string          `json:"name,omitempty"`
	Status  string          `json:"status,omitempty"`
	Command string          `json:"command,omitempty"`
	Output  string          `json:"output,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

func New(cfg Config) *Adapter {
	return &Adapter{cfg: cfg}
}

func Available() bool {
	_, err := ResolveBin("")
	return err == nil
}

func ResolveBin(configured string) (string, error) {
	if strings.TrimSpace(configured) != "" {
		return strings.TrimSpace(configured), nil
	}
	candidates := []string{"codex"}
	if runtime.GOOS == "windows" {
		candidates = []string{"codex.cmd", "codex.exe", "codex"}
	}
	for _, candidate := range candidates {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return path, nil
		}
	}
	return "", errors.New("codex CLI executable not found")
}

func (a *Adapter) Name() string {
	return runtimeName
}

func (a *Adapter) NewSession(ctx context.Context, command protocol.Command, sink agentruntime.Sink) error {
	return a.exec(ctx, command, sink, false)
}

func (a *Adapter) Resume(ctx context.Context, command protocol.Command, sink agentruntime.Sink) error {
	return a.exec(ctx, command, sink, true)
}

func (a *Adapter) Send(ctx context.Context, command protocol.Command, sink agentruntime.Sink) error {
	return a.exec(ctx, command, sink, true)
}

func (a *Adapter) Stop(ctx context.Context, command protocol.Command) error {
	if cancel, ok := a.cancel(command); ok {
		cancel()
	}
	return nil
}

func (a *Adapter) exec(ctx context.Context, command protocol.Command, sink agentruntime.Sink, resume bool) error {
	prompt, err := promptFromCommand(command)
	if err != nil {
		return err
	}
	resolved, err := a.resolve(ctx, command)
	if err != nil {
		return err
	}
	bin, err := ResolveBin(a.cfg.BinPath)
	if err != nil {
		return err
	}
	args, cwd, err := a.buildArgs(command, resolved, resume)
	if err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	a.track(command, cancel)
	defer func() {
		cancel()
		a.untrack(command)
	}()

	cmd := exec.CommandContext(runCtx, bin, args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := io.WriteString(stdin, prompt); err != nil {
		_ = stdin.Close()
		return err
	}
	_ = stdin.Close()

	readErrs := make(chan error, 2)
	go func() { readErrs <- a.readStdout(runCtx, stdout, command, resolved, sink) }()
	go func() { readErrs <- readStderr(runCtx, stderr, command, sink) }()

	waitErr := cmd.Wait()
	for i := 0; i < 2; i++ {
		if err := <-readErrs; err != nil && waitErr == nil {
			waitErr = err
		}
	}
	if runCtx.Err() != nil {
		return runCtx.Err()
	}
	return waitErr
}

func (a *Adapter) buildArgs(command protocol.Command, resolved localstate.CommandContext, resume bool) ([]string, string, error) {
	cwd := firstNonEmpty(resolved.Project.Path, resolved.Session.CWD)
	args := []string{"exec"}
	if resume {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--skip-git-repo-check")
	if a.cfg.Model != "" {
		args = append(args, "-m", a.cfg.Model)
	}
	if a.cfg.Profile != "" {
		args = append(args, "-p", a.cfg.Profile)
	}
	if cwd != "" {
		args = append(args, "-C", cwd)
	}
	if resume {
		nativeSessionID := firstNonEmpty(resolved.Session.ID, command.SessionID)
		if nativeSessionID == "" {
			return nil, "", errors.New("session_id is required for codex resume")
		}
		args = append(args, "--all", nativeSessionID, "-")
		return args, cwd, nil
	}
	args = append(args, "-")
	return args, cwd, nil
}

func (a *Adapter) resolve(ctx context.Context, command protocol.Command) (localstate.CommandContext, error) {
	if a.cfg.State == nil {
		return localstate.CommandContext{}, nil
	}
	if strings.TrimSpace(command.ProjectID) == "" && strings.TrimSpace(command.SessionID) == "" {
		return localstate.CommandContext{}, nil
	}
	return a.cfg.State.ResolveCommandContext(ctx, command)
}

func (a *Adapter) readStdout(ctx context.Context, stdout io.Reader, command protocol.Command, resolved localstate.CommandContext, sink agentruntime.Sink) error {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
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
		if err := a.emitEvent(ctx, command, resolved, event, sink); err != nil {
			return err
		}
	}
	return scanner.Err()
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

func (a *Adapter) emitEvent(ctx context.Context, command protocol.Command, resolved localstate.CommandContext, event execEvent, sink agentruntime.Sink) error {
	switch event.Type {
	case "thread.started":
		return a.emitRuntimeSession(ctx, command, resolved, event.ThreadID, sink)
	case "turn.started":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.started", "raw": event.Raw})
	case "turn.completed":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.completed", "usage": rawJSON(event.Usage), "raw": event.Raw})
	case "turn.failed":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.turn.failed", "message": firstNonEmpty(event.Message, event.Error), "raw": event.Raw})
	case "error":
		return fmt.Errorf("codex CLI error: %s", firstNonEmpty(event.Message, event.Error, string(event.Raw)))
	case "item.started", "item.updated", "item.completed":
		item := decodeItem(event.Item)
		return emitItem(ctx, command, event.Type, item, sink)
	default:
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.event", "type": event.Type, "raw": event.Raw})
	}
}

func emitItem(ctx context.Context, command protocol.Command, phase string, item execItem, sink agentruntime.Sink) error {
	switch item.Type {
	case "agent_message":
		if item.Text == "" {
			return nil
		}
		if phase == "item.completed" {
			if err := emit(ctx, sink, command, "assistant.message.delta", map[string]any{"text": item.Text, "item_id": item.ID, "raw": item.Raw}); err != nil {
				return err
			}
			return emit(ctx, sink, command, "assistant.message.done", map[string]any{"text": item.Text, "item_id": item.ID, "raw": item.Raw})
		}
		return emit(ctx, sink, command, "assistant.message.delta", map[string]any{"text": item.Text, "item_id": item.ID, "raw": item.Raw})
	case "command_execution":
		return emitTool(ctx, command, phase, item, "terminal", sink)
	case "mcp_tool_call":
		return emitTool(ctx, command, phase, item, firstNonEmpty(item.Name, "mcp"), sink)
	case "file_change":
		return emitTool(ctx, command, phase, item, "file_change", sink)
	case "reasoning", "todo_list":
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex." + item.Type, "phase": phase, "item": rawJSON(item.Raw)})
	default:
		return emit(ctx, sink, command, "run.status", map[string]any{"status": "codex.item", "phase": phase, "item": rawJSON(item.Raw)})
	}
}

func emitTool(ctx context.Context, command protocol.Command, phase string, item execItem, toolName string, sink agentruntime.Sink) error {
	payload := map[string]any{
		"id":           item.ID,
		"tool_call_id": item.ID,
		"tool_name":    toolName,
		"name":         firstNonEmpty(item.Name, toolName),
		"status":       item.Status,
		"command":      item.Command,
		"output":       item.Output,
		"text":         item.Text,
		"raw":          rawJSON(item.Raw),
	}
	switch phase {
	case "item.started":
		return emit(ctx, sink, command, "tool.call.started", payload)
	case "item.updated":
		return emit(ctx, sink, command, "tool.call.delta", payload)
	default:
		if err := emit(ctx, sink, command, "tool.call.done", payload); err != nil {
			return err
		}
		if firstNonEmpty(item.Output, item.Text) != "" {
			return emit(ctx, sink, command, "tool.output", payload)
		}
		return nil
	}
}

func (a *Adapter) emitRuntimeSession(ctx context.Context, command protocol.Command, resolved localstate.CommandContext, threadID string, sink agentruntime.Sink) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil
	}
	sessionID := firstNonEmpty(command.SessionID, resolved.Session.ID, threadID)
	projectID := firstNonEmpty(command.ProjectID, resolved.Project.ID, resolved.Session.ProjectID)
	runtimeSession := protocol.RuntimeSession{
		Runtime:         runtimeName,
		ProjectID:       projectID,
		SessionID:       sessionID,
		NativeSessionID: threadID,
		ResumeMode:      "codex_cli_exec",
		LastRunID:       command.RunID,
		UpdatedAt:       time.Now().UTC(),
	}
	if a.cfg.State != nil {
		_ = a.cfg.State.SaveRuntimeSession(ctx, localstate.RuntimeSession{
			SessionID:       runtimeSession.SessionID,
			Runtime:         runtimeSession.Runtime,
			ProjectID:       runtimeSession.ProjectID,
			NativeSessionID: runtimeSession.NativeSessionID,
			ResumeMode:      runtimeSession.ResumeMode,
			LastRunID:       runtimeSession.LastRunID,
			UpdatedAt:       runtimeSession.UpdatedAt,
		})
	}
	return emit(ctx, sink, command, "session.updated", runtimeSession)
}

func decodeItem(raw json.RawMessage) execItem {
	item := execItem{Raw: append(json.RawMessage(nil), raw...)}
	_ = json.Unmarshal(raw, &item)
	return item
}

func promptFromCommand(command protocol.Command) (string, error) {
	if len(command.Payload) == 0 {
		return "", errors.New("prompt payload is required")
	}
	var payload commandPayload
	if err := json.Unmarshal(command.Payload, &payload); err != nil {
		return "", err
	}
	prompt := firstNonEmpty(payload.Prompt, payload.Text)
	if prompt == "" {
		return "", errors.New("prompt is required")
	}
	return prompt, nil
}

func emit(ctx context.Context, sink agentruntime.Sink, command protocol.Command, kind string, payload any) error {
	if sink == nil {
		return nil
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      kind,
		Payload:   protocol.Raw(payload),
	})
}

func (a *Adapter) track(command protocol.Command, cancel context.CancelFunc) {
	runID := strings.TrimSpace(command.RunID)
	if runID == "" || cancel == nil {
		return
	}
	a.active.mu.Lock()
	defer a.active.mu.Unlock()
	if a.active.cancelBy == nil {
		a.active.cancelBy = map[string]context.CancelFunc{}
	}
	if a.active.sessionBy == nil {
		a.active.sessionBy = map[string]string{}
	}
	a.active.cancelBy[runID] = cancel
	if command.SessionID != "" {
		a.active.sessionBy[command.SessionID] = runID
	}
}

func (a *Adapter) untrack(command protocol.Command) {
	runID := strings.TrimSpace(command.RunID)
	if runID == "" {
		return
	}
	a.active.mu.Lock()
	defer a.active.mu.Unlock()
	delete(a.active.cancelBy, runID)
	if command.SessionID != "" && a.active.sessionBy[command.SessionID] == runID {
		delete(a.active.sessionBy, command.SessionID)
	}
}

func (a *Adapter) cancel(command protocol.Command) (context.CancelFunc, bool) {
	a.active.mu.Lock()
	defer a.active.mu.Unlock()
	runID := strings.TrimSpace(command.RunID)
	if runID == "" && command.SessionID != "" {
		runID = a.active.sessionBy[command.SessionID]
	}
	cancel := a.active.cancelBy[runID]
	return cancel, cancel != nil
}

func rawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
