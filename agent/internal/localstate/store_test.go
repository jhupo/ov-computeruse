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
