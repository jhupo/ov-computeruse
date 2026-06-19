package codexcli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
)

type captureSink struct {
	events []protocol.RunEvent
}

func (s *captureSink) Emit(_ context.Context, event protocol.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}

type refreshFunc func(context.Context) error

func (f refreshFunc) RefreshCodexIndex(ctx context.Context) error {
	return f(ctx)
}

func TestBuildArgsForNewSession(t *testing.T) {
	adapter := New(Config{})
	args, cwd, err := adapter.buildArgs(protocol.Command{}, localstate.CommandContext{
		Project: localstate.ProjectRecord{Path: `C:\repo`},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	want := []string{"exec", "--json", "--skip-git-repo-check", "-c", "approval_policy=never", "-C", `C:\repo`, "-"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
	if cwd != `C:\repo` {
		t.Fatalf("cwd = %q, want C:\\repo", cwd)
	}
}

func TestBuildArgsForResume(t *testing.T) {
	adapter := New(Config{Model: "gpt-5.1-codex-max", Profile: "work"})
	args, _, err := adapter.buildArgs(protocol.Command{SessionID: "thread_1"}, localstate.CommandContext{}, true)
	if err != nil {
		t.Fatalf("build resume args: %v", err)
	}
	want := []string{"exec", "resume", "--json", "--skip-git-repo-check", "-c", "approval_policy=never", "-m", "gpt-5.1-codex-max", "-p", "work", "--all", "thread_1", "-"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", args, want)
	}
}

func TestBuildArgsForResumePrefersRuntimeNativeSessionID(t *testing.T) {
	adapter := New(Config{})
	args, _, err := adapter.buildArgs(protocol.Command{SessionID: "dash_session"}, localstate.CommandContext{
		Session:        localstate.SessionRecord{ID: "indexed_session"},
		RuntimeSession: localstate.RuntimeSession{SessionID: "runtime_session", NativeSessionID: "native_thread"},
	}, true)
	if err != nil {
		t.Fatalf("build resume args: %v", err)
	}
	if got := args[len(args)-2]; got != "native_thread" {
		t.Fatalf("resume session arg = %q, want native_thread; args=%#v", got, args)
	}
}

func TestResolveRefreshesMissingLocalIndex(t *testing.T) {
	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	root := t.TempDir()
	projectPath := filepath.Join(root, "repo")
	refreshed := false
	adapter := New(Config{
		State: state,
		IndexRefresher: refreshFunc(func(ctx context.Context) error {
			refreshed = true
			_, err := state.SaveScanResult(ctx, codexscan.Result{
				Projects: []codexscan.Project{{
					ID:           "project_1",
					Name:         "repo",
					Path:         projectPath,
					LastActiveAt: time.Now().UTC(),
				}},
				Sessions: []codexscan.Session{{
					ID:        "session_1",
					ProjectID: "project_1",
					Path:      filepath.Join(root, "history.jsonl"),
					CWD:       projectPath,
					UpdatedAt: time.Now().UTC(),
				}},
			})
			return err
		}),
	})
	resolved, err := adapter.resolve(context.Background(), protocol.Command{SessionID: "session_1"})
	if err != nil {
		t.Fatalf("resolve command context: %v", err)
	}
	if !refreshed {
		t.Fatal("expected missing local index to trigger refresh")
	}
	if resolved.Session.ID != "session_1" || resolved.Project.ID != "project_1" {
		t.Fatalf("resolved context = %+v", resolved)
	}
}

func TestReadStdoutMapsCodexExecEvents(t *testing.T) {
	adapter := New(Config{})
	command := protocol.Command{
		CommandID: "cmd_1",
		RunID:     "run_1",
		ProjectID: "project_1",
		Payload:   protocol.Raw(map[string]string{"prompt": "hi"}),
	}
	input := strings.Join([]string{
		`{"type":"thread.started","thread_id":"019edb96-e4c5-7503-9d11-f3e7c4b2c704"}`,
		`{"type":"turn.started"}`,
		`{"type":"item.completed","item":{"id":"item_0","type":"agent_message","text":"probe-ok"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":2}}`,
	}, "\n")
	sink := &captureSink{}
	completion := &completionSignal{}
	if err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink, completion); err != nil && err != io.EOF {
		t.Fatalf("read stdout: %v", err)
	}
	if !completion.Done() {
		t.Fatal("expected completion signal after turn.completed")
	}

	kinds := make([]string, 0, len(sink.events))
	for _, event := range sink.events {
		kinds = append(kinds, event.Kind)
	}
	wantKinds := []string{"session.updated", "run.status", "assistant.message.done", "run.status"}
	if strings.Join(kinds, "\x00") != strings.Join(wantKinds, "\x00") {
		t.Fatalf("event kinds = %#v, want %#v", kinds, wantKinds)
	}

	var runtimeSession protocol.RuntimeSession
	if err := json.Unmarshal(sink.events[0].Payload, &runtimeSession); err != nil {
		t.Fatalf("decode runtime session: %v", err)
	}
	if runtimeSession.Runtime != protocol.RuntimeCodexCLI || runtimeSession.NativeSessionID == "" {
		t.Fatalf("runtime session = %+v", runtimeSession)
	}
	if sink.events[0].SessionID != runtimeSession.SessionID || sink.events[0].ProjectID != runtimeSession.ProjectID {
		t.Fatalf("session event target = project %q session %q, want project %q session %q", sink.events[0].ProjectID, sink.events[0].SessionID, runtimeSession.ProjectID, runtimeSession.SessionID)
	}
}

func TestReadStdoutMapsCodexToolItems(t *testing.T) {
	adapter := New(Config{})
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	input := strings.Join([]string{
		`{"type":"item.started","item":{"id":"cmd","type":"command_execution","command":"git status","status":"running"}}`,
		`{"type":"item.updated","item":{"id":"cmd","type":"command_execution","command":"git status","aggregated_output":"still running","status":"running"}}`,
		`{"type":"item.completed","item":{"id":"cmd","type":"command_execution","command":"git status","aggregated_output":"clean","exit_code":0,"status":"succeeded"}}`,
		`{"type":"item.completed","item":{"id":"mcp","type":"mcp_tool_call","server":"fs","tool":"read","arguments":{"path":"README.md"},"result":{"content":"ok"},"status":"succeeded"}}`,
		`{"type":"item.completed","item":{"id":"file","type":"file_change","changes":[{"path":"a.go","kind":"modified"}],"status":"succeeded"}}`,
		`{"type":"item.updated","item":{"id":"todo","type":"todo_list","items":[{"text":"ship","completed":false}]}}`,
		`{"type":"turn.failed","error":{"message":"boom"}}`,
	}, "\n")
	sink := &captureSink{}
	completion := &completionSignal{}
	if err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink, completion); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("read stdout error = %v, want turn failure", err)
	}
	if !completion.Done() {
		t.Fatal("expected completion signal after turn.failed")
	}

	kinds := make([]string, 0, len(sink.events))
	for _, event := range sink.events {
		kinds = append(kinds, event.Kind)
	}
	wantKinds := []string{
		"tool.call.started",
		"tool.call.delta",
		"terminal.output",
		"tool.call.done",
		"tool.output",
		"tool.call.done",
		"tool.output",
		"tool.call.done",
		"tool.output",
		"run.status",
		"run.status",
	}
	if strings.Join(kinds, "\x00") != strings.Join(wantKinds, "\x00") {
		t.Fatalf("event kinds = %#v, want %#v", kinds, wantKinds)
	}
	assertPayloadString(t, sink.events[2].Payload, "tool_call_id", "cmd")
	assertPayloadString(t, sink.events[4].Payload, "output", "clean")
	assertPayloadString(t, sink.events[6].Payload, "tool", "read")
	assertPayloadString(t, sink.events[10].Payload, "message", "boom")
}

func TestReadStdoutReturnsErrorForTurnFailed(t *testing.T) {
	adapter := New(Config{})
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	sink := &captureSink{}
	completion := &completionSignal{}
	err := adapter.readStdout(context.Background(), strings.NewReader(`{"type":"turn.failed","error":{"message":"boom"}}`), command, localstate.CommandContext{}, sink, completion)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("read stdout error = %v, want turn failure", err)
	}
	if !completion.Done() {
		t.Fatal("expected completion signal after turn.failed")
	}
	if len(sink.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(sink.events))
	}
	if sink.events[0].Kind != "run.status" {
		t.Fatalf("event kind = %q, want run.status", sink.events[0].Kind)
	}
	assertPayloadString(t, sink.events[0].Payload, "status", "codex.turn.failed")
	assertPayloadString(t, sink.events[0].Payload, "message", "boom")
}

func TestReadStdoutEmitsTerminalOutputDelta(t *testing.T) {
	adapter := New(Config{})
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	input := strings.Join([]string{
		`{"type":"item.updated","item":{"id":"cmd","type":"command_execution","command":"printf","aggregated_output":"hello","status":"running"}}`,
		`{"type":"item.updated","item":{"id":"cmd","type":"command_execution","command":"printf","aggregated_output":"hello world","status":"running"}}`,
	}, "\n")
	sink := &captureSink{}
	if err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink, &completionSignal{}); err != nil && err != io.EOF {
		t.Fatalf("read stdout: %v", err)
	}
	var terminal []protocol.RunEvent
	for _, event := range sink.events {
		if event.Kind == "terminal.output" {
			terminal = append(terminal, event)
		}
	}
	if len(terminal) != 2 {
		t.Fatalf("terminal output count = %d, want 2; events=%+v", len(terminal), sink.events)
	}
	assertPayloadString(t, terminal[0].Payload, "text", "hello")
	assertPayloadString(t, terminal[1].Payload, "text", " world")
}

func TestReadStdoutEmitsAssistantMessageDelta(t *testing.T) {
	adapter := New(Config{})
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	input := strings.Join([]string{
		`{"type":"item.updated","item":{"id":"msg","type":"agent_message","text":"hello"}}`,
		`{"type":"item.updated","item":{"id":"msg","type":"agent_message","text":"hello world"}}`,
		`{"type":"item.completed","item":{"id":"msg","type":"agent_message","text":"hello world"}}`,
	}, "\n")
	sink := &captureSink{}
	if err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink, &completionSignal{}); err != nil && err != io.EOF {
		t.Fatalf("read stdout: %v", err)
	}
	if len(sink.events) != 3 {
		t.Fatalf("event count = %d, want 3; events=%+v", len(sink.events), sink.events)
	}
	if sink.events[0].Kind != "assistant.message.delta" || sink.events[1].Kind != "assistant.message.delta" || sink.events[2].Kind != "assistant.message.done" {
		t.Fatalf("event kinds = %#v", []string{sink.events[0].Kind, sink.events[1].Kind, sink.events[2].Kind})
	}
	assertPayloadString(t, sink.events[0].Payload, "text", "hello")
	assertPayloadString(t, sink.events[1].Payload, "text", " world")
	assertPayloadString(t, sink.events[2].Payload, "text", "hello world")
}

func TestEmitProcessExitedMarksCanceled(t *testing.T) {
	sink := &captureSink{}
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	if err := emitProcessExited(context.Background(), sink, command, errors.New("killed"), context.Canceled); err != nil {
		t.Fatalf("emit process exited: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(sink.events))
	}
	assertPayloadString(t, sink.events[0].Payload, "status", "codex.process.exited")
	assertPayloadString(t, sink.events[0].Payload, "error", context.Canceled.Error())
	assertPayloadBool(t, sink.events[0].Payload, "canceled", true)
}

func TestReadStdoutFailsCodexApprovalRequestsAsUnsupportedStatus(t *testing.T) {
	adapter := New(Config{})
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	input := `{"type":"codex/event/exec_approval_request","id":"approval_1","command":"git status","cwd":"C:\\repo"}`
	sink := &captureSink{}
	err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink, &completionSignal{})
	if !errors.Is(err, errUnsupportedApproval) {
		t.Fatalf("read stdout error = %v, want unsupported approval", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(sink.events))
	}
	if sink.events[0].Kind != "run.status" {
		t.Fatalf("event kind = %q, want run.status", sink.events[0].Kind)
	}
	assertPayloadString(t, sink.events[0].Payload, "command", "git status")
	assertPayloadString(t, sink.events[0].Payload, "status", "codex.approval.unsupported")
}

func TestBinCandidatesPreferWindowsLaunchers(t *testing.T) {
	got := binCandidates("windows")
	want := []string{"codex.exe", "codex.cmd", "codex"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("windows candidates = %#v, want %#v", got, want)
	}
}

func TestProcessStatusEvents(t *testing.T) {
	command := protocol.Command{CommandID: "cmd_1", RunID: "run_1"}
	sink := &captureSink{}
	if err := emitProcessStarted(context.Background(), sink, command, "codex.cmd", []string{"exec", "--json", "-"}, `C:\repo`); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if err := emitProcessExited(context.Background(), sink, command, nil, nil); err != nil {
		t.Fatalf("emit exited: %v", err)
	}
	if len(sink.events) != 2 {
		t.Fatalf("event count = %d, want 2", len(sink.events))
	}
	assertPayloadString(t, sink.events[0].Payload, "status", "codex.process.started")
	assertPayloadString(t, sink.events[1].Payload, "status", "codex.process.exited")
}

func assertPayloadString(t *testing.T, raw json.RawMessage, key, want string) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got, _ := payload[key].(string); got != want {
		t.Fatalf("payload[%s] = %q, want %q; payload=%s", key, got, want, raw)
	}
}

func assertPayloadBool(t *testing.T, raw json.RawMessage, key string, want bool) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if got, _ := payload[key].(bool); got != want {
		t.Fatalf("payload[%s] = %v, want %v; payload=%s", key, got, want, raw)
	}
}
