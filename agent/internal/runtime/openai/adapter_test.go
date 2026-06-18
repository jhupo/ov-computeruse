package openai

import (
	"context"
	"path/filepath"
	"testing"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
)

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
