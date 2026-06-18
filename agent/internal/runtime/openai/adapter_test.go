package openai

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
)

type captureSink struct {
	events []protocol.RunEvent
}

func (s *captureSink) Emit(ctx context.Context, event protocol.RunEvent) error {
	s.events = append(s.events, event)
	return nil
}

func TestAdapterName(t *testing.T) {
	if got := New(Config{}).Name(); got != runtimeName {
		t.Fatalf("runtime name = %q, want %q", got, runtimeName)
	}
}

func TestResumeInputPrefersPreviousResponseID(t *testing.T) {
	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()

	err = state.SaveRuntimeSession(context.Background(), localstate.RuntimeSession{
		SessionID:      "session_remote",
		Runtime:        runtimeName,
		LastResponseID: "resp_existing",
		ResumeMode:     "previous_response_id",
	})
	if err != nil {
		t.Fatalf("save runtime session: %v", err)
	}

	adapter := New(Config{State: state})
	resume, err := adapter.resumeInput(context.Background(), protocol.Command{SessionID: "session_remote"}, "continue this", true)
	if err != nil {
		t.Fatalf("resume input: %v", err)
	}

	if resume.PreviousResponseID != "resp_existing" {
		t.Fatalf("previous response id = %q, want resp_existing", resume.PreviousResponseID)
	}
	if resume.ResumeMode != "previous_response_id" {
		t.Fatalf("resume mode = %q, want previous_response_id", resume.ResumeMode)
	}
	if resume.Input != "continue this" {
		t.Fatalf("input = %q, want original prompt", resume.Input)
	}
}

func TestEmitRuntimeSessionSeparatesNativeResponseIdentity(t *testing.T) {
	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	adapter := New(Config{State: state})
	sink := &captureSink{}
	command := protocol.Command{RunID: "run_1", SessionID: "session_1", ProjectID: "project_1"}
	if err := adapter.emitRuntimeSession(context.Background(), sink, command, "session.updated", "resp_1", "previous_response_id"); err != nil {
		t.Fatalf("emit runtime session: %v", err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("event count = %d, want 1", len(sink.events))
	}
	session, err := protocol.Decode[protocol.RuntimeSession](sink.events[0].Payload)
	if err != nil {
		t.Fatalf("decode runtime session: %v", err)
	}
	if session.SessionID != "session_1" || session.NativeSessionID != "responses:resp_1" || session.LastResponseID != "resp_1" {
		t.Fatalf("runtime session = %+v", session)
	}
	local, err := state.RuntimeSession(context.Background(), "session_1", runtimeName)
	if err != nil {
		t.Fatalf("local runtime session: %v", err)
	}
	if local.NativeSessionID != "responses:resp_1" || local.LastResponseID != "resp_1" {
		t.Fatalf("local runtime session = %+v", local)
	}
	byResponse, err := state.RuntimeSession(context.Background(), "resp_1", runtimeName)
	if err != nil {
		t.Fatalf("local runtime session by response id: %v", err)
	}
	if byResponse.SessionID != "session_1" {
		t.Fatalf("runtime session by response id = %+v", byResponse)
	}
}

func TestPromptWithProjectContext(t *testing.T) {
	prompt := promptWithProjectContext("inspect the repo", localstate.CommandContext{
		Project: localstate.ProjectRecord{ID: "project_1", Name: "repo", Path: `/tmp/repo`, GitBranch: "main"},
		Session: localstate.SessionRecord{ID: "session_1", CWD: `/tmp/repo`},
	})
	for _, want := range []string{"project_id: project_1", "project_path: /tmp/repo", "session_id: session_1", "USER PROMPT:\ninspect the repo"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
