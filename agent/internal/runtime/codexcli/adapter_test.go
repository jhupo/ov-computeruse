package codexcli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

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

func TestBuildArgsForNewSession(t *testing.T) {
	adapter := New(Config{})
	args, cwd, err := adapter.buildArgs(protocol.Command{}, localstate.CommandContext{
		Project: localstate.ProjectRecord{Path: `C:\repo`},
	}, false)
	if err != nil {
		t.Fatalf("build args: %v", err)
	}
	want := []string{"exec", "--json", "--skip-git-repo-check", "-C", `C:\repo`, "-"}
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
	want := []string{"exec", "resume", "--json", "--skip-git-repo-check", "-m", "gpt-5.1-codex-max", "-p", "work", "--all", "thread_1", "-"}
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
	if err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink); err != nil && err != io.EOF {
		t.Fatalf("read stdout: %v", err)
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
	if err := adapter.readStdout(context.Background(), strings.NewReader(input), command, localstate.CommandContext{}, sink); err != nil && err != io.EOF {
		t.Fatalf("read stdout: %v", err)
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

func TestBinCandidatesPreferWindowsLaunchers(t *testing.T) {
	got := binCandidates("windows")
	want := []string{"codex.cmd", "codex.exe", "codex"}
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
	if err := emitProcessExited(context.Background(), sink, command, nil); err != nil {
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
