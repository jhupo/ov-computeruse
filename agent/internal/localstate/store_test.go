package localstate

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/protocol"
)

func TestResolveCommandContext(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	root := t.TempDir()
	projectPath := filepath.Join(root, "repo")
	sessionPath := filepath.Join(root, "history.jsonl")

	_, err = state.SaveScanResult(context.Background(), codexscan.Result{
		Projects: []codexscan.Project{{
			ID:           "project_1",
			Name:         "repo",
			Path:         projectPath,
			LastActiveAt: time.Now().UTC(),
		}},
		Sessions: []codexscan.Session{{
			ID:        "session_1",
			ProjectID: "project_1",
			Path:      sessionPath,
			CWD:       projectPath,
			UpdatedAt: time.Now().UTC(),
		}},
	})
	if err != nil {
		t.Fatalf("save scan result: %v", err)
	}

	resolved, err := state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "session_1"})
	if err != nil {
		t.Fatalf("resolve command context: %v", err)
	}
	if resolved.Session.ID != "session_1" {
		t.Fatalf("session id = %q, want session_1", resolved.Session.ID)
	}
	if resolved.Project.ID != "project_1" {
		t.Fatalf("project id = %q, want project_1", resolved.Project.ID)
	}

	_, err = state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "session_1", ProjectID: "other_project"})
	if err == nil {
		t.Fatal("expected project/session mismatch error")
	}
}

func TestResolveCommandContextAcceptsRuntimeSession(t *testing.T) {
	state, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	root := t.TempDir()
	projectPath := filepath.Join(root, "repo")
	updatedAt := time.Now().UTC().Add(-time.Minute)

	_, err = state.SaveScanResult(context.Background(), codexscan.Result{
		Projects: []codexscan.Project{{
			ID:           "project_1",
			Name:         "repo",
			Path:         projectPath,
			LastActiveAt: updatedAt,
		}},
	})
	if err != nil {
		t.Fatalf("save scan result: %v", err)
	}
	err = state.SaveRuntimeSession(context.Background(), RuntimeSession{
		SessionID:       "resp_session",
		Runtime:         "openai.responses",
		ProjectID:       "project_1",
		NativeSessionID: "resp_session",
		LastResponseID:  "resp_latest",
		ResumeMode:      "previous_response_id",
		UpdatedAt:       updatedAt,
	})
	if err != nil {
		t.Fatalf("save runtime session: %v", err)
	}

	resolved, err := state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "resp_session"})
	if err != nil {
		t.Fatalf("resolve runtime command context: %v", err)
	}
	if resolved.Session.ID != "resp_session" || resolved.Session.IDSource != "runtime_session" {
		t.Fatalf("runtime session = %+v, want resp_session runtime_session", resolved.Session)
	}
	if resolved.Project.ID != "project_1" {
		t.Fatalf("project id = %q, want project_1", resolved.Project.ID)
	}

	_, err = state.ResolveCommandContext(context.Background(), protocol.Command{SessionID: "resp_session", ProjectID: "other_project"})
	if err == nil {
		t.Fatal("expected runtime project/session mismatch error")
	}

	runtimeSessions, err := state.RuntimeSessions(context.Background())
	if err != nil {
		t.Fatalf("runtime sessions: %v", err)
	}
	if len(runtimeSessions) != 1 {
		t.Fatalf("runtime session count = %d, want 1", len(runtimeSessions))
	}
	if runtimeSessions[0].ProjectID != "project_1" || runtimeSessions[0].LastRunID != "" || runtimeSessions[0].UpdatedAt.IsZero() {
		t.Fatalf("runtime session not fully preserved: %+v", runtimeSessions[0])
	}
}
