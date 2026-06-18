package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"time"

	"ov-computeruse/agent/internal/protocol"
	agentruntime "ov-computeruse/agent/internal/runtime"
)

type CallKind string

const (
	CallKindFunction   CallKind = "function_call"
	CallKindLocalShell CallKind = "local_shell_call"
)

type Call struct {
	Kind      CallKind        `json:"kind"`
	ID        string          `json:"id,omitempty"`
	CallID    string          `json:"call_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type Result struct {
	CallID   string          `json:"call_id"`
	Output   string          `json:"output"`
	Approved bool            `json:"approved,omitempty"`
	Error    string          `json:"error,omitempty"`
	Payload  json.RawMessage `json:"payload,omitempty"`
}

type Config struct {
	AllowLocalShell       bool
	WorkspaceRoots        []string
	WorkspaceRootProvider func(context.Context) ([]string, error)
	MaxOutputBytes        int
	DefaultTimeout        time.Duration
	MaxTimeout            time.Duration
}

type Executor struct {
	cfg Config
}

type LocalShellAction struct {
	Command          []string          `json:"command"`
	Env              map[string]string `json:"env,omitempty"`
	Type             string            `json:"type,omitempty"`
	TimeoutMs        int64             `json:"timeout_ms,omitempty"`
	User             string            `json:"user,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty"`
}

func NewExecutor(cfg Config) Executor {
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 256 << 10
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = 2 * time.Minute
	}
	cfg.WorkspaceRoots = cleanRoots(cfg.WorkspaceRoots)
	return Executor{cfg: cfg}
}

func (e Executor) Execute(ctx context.Context, sink agentruntime.Sink, command protocol.Command, call Call) (Result, error) {
	if strings.TrimSpace(call.CallID) == "" {
		return Result{}, errors.New("tool call id is required")
	}
	normalized := normalizeCall(call)
	result, err := e.executeNormalized(ctx, sink, command, normalized)
	if err != nil {
		result = Result{CallID: normalized.CallID, Error: err.Error(), Output: errorOutput(normalized, err)}
	}
	if strings.TrimSpace(result.Output) == "" {
		result.Output = resultOutput(result)
	}
	if result.CallID == "" {
		result.CallID = normalized.CallID
	}
	if err := emitToolOutput(ctx, sink, command, normalized, "completed", result); err != nil {
		return result, err
	}
	return result, err
}

func (e Executor) executeNormalized(ctx context.Context, sink agentruntime.Sink, command protocol.Command, call Call) (Result, error) {
	switch call.Kind {
	case CallKindLocalShell:
		return e.executeLocalShell(ctx, sink, command, call)
	case CallKindFunction:
		return rejectFunction(call), nil
	default:
		return rejectFunction(call), nil
	}
}

func (e Executor) executeLocalShell(ctx context.Context, sink agentruntime.Sink, command protocol.Command, call Call) (Result, error) {
	if !e.cfg.AllowLocalShell {
		return rejection(call, "local shell execution is disabled by agent policy"), nil
	}
	action, err := parseLocalShellAction(call.Arguments)
	if err != nil {
		return rejection(call, err.Error()), nil
	}
	request := protocol.ApprovalRequest{
		ID:          protocol.NewID("apr"),
		RunID:       command.RunID,
		ProjectID:   command.ProjectID,
		SessionID:   command.SessionID,
		Category:    "local_shell",
		Action:      "local_shell",
		RiskLevel:   "high",
		Description: "Local shell execution requires approval",
		Payload:     protocol.Raw(map[string]any{"call": call, "action": action}),
		At:          time.Now().UTC(),
	}
	waiter, ok := sink.(agentruntime.ApprovalWaiter)
	if !ok {
		return rejection(call, "local shell execution is unavailable because approvals are not wired"), nil
	}
	decision, err := waiter.AwaitApproval(ctx, request)
	if err != nil {
		return Result{}, err
	}
	approved := isApproved(decision.Decision)
	if !approved {
		return rejection(call, firstNonEmpty(decision.Reason, "local shell execution was rejected")), nil
	}
	workspace, err := e.resolveWorkingDirectory(ctx, action.WorkingDirectory)
	if err != nil {
		return rejection(call, err.Error()), nil
	}
	return e.runLocalShell(ctx, sink, command, call, action, workspace)
}

func rejectFunction(call Call) Result {
	return rejection(call, "function tool is not registered in this agent build")
}

func (e Executor) runLocalShell(ctx context.Context, sink agentruntime.Sink, command protocol.Command, call Call, action LocalShellAction, cwd string) (Result, error) {
	timeout := e.timeout(action.TimeoutMs)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, action.Command[0], action.Command[1:]...)
	cmd.Dir = cwd
	cmd.Env = append(cmd.Environ(), shellEnv(action.Env)...)
	prepareCommand(cmd)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &limitedBuffer{Buffer: &stdout, Limit: e.cfg.MaxOutputBytes}
	cmd.Stderr = &limitedBuffer{Buffer: &stderr, Limit: e.cfg.MaxOutputBytes}
	startedAt := time.Now().UTC()
	err := startCommand(cmd)
	if err == nil {
		err = waitCommand(runCtx, cmd)
	}
	finishedAt := time.Now().UTC()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	timedOut := runCtx.Err() == context.DeadlineExceeded
	output := map[string]any{
		"ok":                err == nil,
		"call_id":           call.CallID,
		"command":           action.Command,
		"cwd":               cwd,
		"exit_code":         exitCode,
		"stdout":            stdout.String(),
		"stderr":            stderr.String(),
		"timed_out":         timedOut,
		"started_at":        startedAt,
		"finished_at":       finishedAt,
		"max_output_bytes":  e.cfg.MaxOutputBytes,
		"duration_millis":   finishedAt.Sub(startedAt).Milliseconds(),
		"workspace_checked": true,
	}
	if err != nil {
		output["error"] = err.Error()
	}
	if emitErr := emitTerminalOutput(ctx, sink, command, call, output); emitErr != nil {
		return Result{}, emitErr
	}
	raw := protocol.Raw(output)
	result := Result{CallID: call.CallID, Output: string(raw), Approved: true, Payload: raw}
	if err != nil {
		result.Error = err.Error()
	}
	return result, nil
}

func parseLocalShellAction(raw json.RawMessage) (LocalShellAction, error) {
	var action LocalShellAction
	if len(raw) == 0 {
		return action, errors.New("local shell action is required")
	}
	if err := json.Unmarshal(raw, &action); err != nil {
		return action, err
	}
	if len(action.Command) == 0 || strings.TrimSpace(action.Command[0]) == "" {
		return action, errors.New("local shell command is required")
	}
	return action, nil
}

func (e Executor) resolveWorkingDirectory(ctx context.Context, requested string) (string, error) {
	roots, err := e.workspaceRoots(ctx)
	if err != nil {
		return "", err
	}
	cwd := strings.TrimSpace(requested)
	if cwd == "" {
		if len(roots) == 1 {
			return roots[0], nil
		}
		return "", errors.New("local shell working_directory is required")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	realPath, err := realDirectory(abs)
	if err != nil {
		return "", err
	}
	for _, root := range roots {
		if pathWithin(root, realPath) {
			return realPath, nil
		}
	}
	return "", errors.New("local shell working_directory is outside indexed project roots")
}

func (e Executor) workspaceRoots(ctx context.Context) ([]string, error) {
	if e.cfg.WorkspaceRootProvider == nil {
		return e.cfg.WorkspaceRoots, nil
	}
	roots, err := e.cfg.WorkspaceRootProvider(ctx)
	if err != nil {
		return nil, err
	}
	return cleanRoots(roots), nil
}

func (e Executor) timeout(timeoutMs int64) time.Duration {
	timeout := e.cfg.DefaultTimeout
	if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	if timeout > e.cfg.MaxTimeout {
		return e.cfg.MaxTimeout
	}
	return timeout
}

func rejection(call Call, reason string) Result {
	payload := protocol.Raw(map[string]any{
		"ok":      false,
		"error":   reason,
		"tool":    call.Name,
		"kind":    call.Kind,
		"call_id": call.CallID,
	})
	return Result{CallID: call.CallID, Output: string(payload), Error: reason, Payload: payload}
}

func normalizeCall(call Call) Call {
	call.CallID = strings.TrimSpace(call.CallID)
	call.ID = strings.TrimSpace(call.ID)
	call.Name = strings.TrimSpace(call.Name)
	if call.Name == "" && call.Kind == CallKindLocalShell {
		call.Name = "local_shell"
	}
	if call.Name == "" {
		call.Name = string(call.Kind)
	}
	return call
}

func emitToolOutput(ctx context.Context, sink agentruntime.Sink, command protocol.Command, call Call, status string, result any) error {
	if sink == nil {
		return nil
	}
	payload := map[string]any{
		"tool_call_id": call.CallID,
		"call_id":      call.CallID,
		"type":         call.Kind,
		"name":         call.Name,
		"status":       status,
	}
	if arguments := decodeJSON(call.Arguments); arguments != nil {
		payload["arguments"] = arguments
	}
	if result != nil {
		payload["output"] = result
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      "tool.output",
		Payload:   protocol.Raw(payload),
	})
}

func emitTerminalOutput(ctx context.Context, sink agentruntime.Sink, command protocol.Command, call Call, output map[string]any) error {
	if sink == nil {
		return nil
	}
	payload := map[string]any{
		"tool_call_id": call.CallID,
		"call_id":      call.CallID,
		"command":      output["command"],
		"cwd":          output["cwd"],
		"exit_code":    output["exit_code"],
		"stdout":       output["stdout"],
		"stderr":       output["stderr"],
		"timed_out":    output["timed_out"],
		"started_at":   output["started_at"],
		"finished_at":  output["finished_at"],
	}
	return sink.Emit(ctx, protocol.RunEvent{
		EventID:   protocol.NewID("evt"),
		RunID:     command.RunID,
		CommandID: command.CommandID,
		ProjectID: command.ProjectID,
		SessionID: command.SessionID,
		Kind:      "terminal.output",
		Payload:   protocol.Raw(payload),
	})
}

func decodeJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return string(raw)
	}
	return value
}

func errorOutput(call Call, err error) string {
	return resultOutput(Result{CallID: call.CallID, Error: err.Error()})
}

func resultOutput(result Result) string {
	if len(result.Payload) > 0 {
		return string(result.Payload)
	}
	raw := protocol.Raw(map[string]any{
		"ok":      result.Error == "",
		"error":   result.Error,
		"call_id": result.CallID,
	})
	return string(raw)
}

func isApproved(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "approved", "approve", "accepted", "allow", "allowed":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cleanRoots(roots []string) []string {
	out := make([]string, 0, len(roots))
	seen := map[string]struct{}{}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		abs = filepath.Clean(abs)
		realRoot, err := realDirectory(abs)
		if err != nil {
			continue
		}
		key := comparablePath(realRoot)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, realRoot)
	}
	return out
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if samePath(root, path) {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func realDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("local shell working_directory is not a directory")
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(realPath), nil
}

func samePath(left, right string) bool {
	if goruntime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func comparablePath(path string) string {
	if goruntime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func shellEnv(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" || strings.Contains(key, "=") {
			continue
		}
		out = append(out, key+"="+value)
	}
	return out
}

type limitedBuffer struct {
	*bytes.Buffer
	Limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b == nil || b.Buffer == nil {
		return len(p), nil
	}
	if b.Limit <= 0 {
		return b.Buffer.Write(p)
	}
	remaining := b.Limit - b.Buffer.Len()
	if remaining <= 0 {
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		return len(p), nil
	}
	_, _ = b.Buffer.Write(p)
	return len(p), nil
}
