package codexhistory

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/protocol"
)

func TestSyncRunEventAppendsReadableCodexMessages(t *testing.T) {
	ctx := context.Background()
	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	_, err = state.SaveScanResult(ctx, codexscan.Result{
		Sessions: []codexscan.Session{{
			ID:        "session_1",
			Path:      sessionPath,
			UpdatedAt: time.Now().UTC(),
		}},
	})
	if err != nil {
		t.Fatalf("save scan result: %v", err)
	}
	writer := New(state)
	for _, event := range []protocol.RunEvent{
		{EventID: "evt_user", SessionID: "session_1", RunID: "run_1", Kind: "user.message", Payload: protocol.Raw(map[string]string{"text": "hello from web"}), At: time.Now().UTC()},
		{EventID: "evt_assistant", SessionID: "session_1", RunID: "run_1", Kind: "assistant.message.done", Payload: protocol.Raw(map[string]string{"text": "hello from codex"}), At: time.Now().UTC()},
	} {
		if err := writer.SyncRunEvent(ctx, event); err != nil {
			t.Fatalf("sync event %s: %v", event.EventID, err)
		}
	}
	messages, err := codexscan.ReadSessionMessages(ctx, sessionPath, 10, 64<<10)
	if err != nil {
		t.Fatalf("read session messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("message count = %d, want 2: %+v", len(messages), messages)
	}
	if messages[0].Role != "user" || messages[0].Text != "hello from web" {
		t.Fatalf("user message = %+v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Text != "hello from codex" {
		t.Fatalf("assistant message = %+v", messages[1])
	}
	if err := writer.SyncRunEvent(ctx, protocol.RunEvent{EventID: "evt_user", SessionID: "session_1", RunID: "run_1", Kind: "user.message", Payload: protocol.Raw(map[string]string{"text": "duplicate"}), At: time.Now().UTC()}); err != nil {
		t.Fatalf("sync duplicate: %v", err)
	}
	messages, err = codexscan.ReadSessionMessages(ctx, sessionPath, 10, 64<<10)
	if err != nil {
		t.Fatalf("read session messages after duplicate: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("duplicate sync changed message count: %+v", messages)
	}
}

func TestSyncRunEventCreatesMissingCodexSession(t *testing.T) {
	ctx := context.Background()
	state, err := localstate.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	defer state.Close()
	root := t.TempDir()
	if err := state.SaveCodexRoots(ctx, []codexscan.Root{{Path: root, Kind: "codex_home", Source: "test", Exists: true}}); err != nil {
		t.Fatalf("save root: %v", err)
	}
	projectPath := filepath.Join(t.TempDir(), "project")
	_, err = state.SaveScanResult(ctx, codexscan.Result{
		Roots: []codexscan.Root{{Path: root, Kind: "codex_home", Source: "test", Exists: true}},
		Projects: []codexscan.Project{{
			ID:           "project_1",
			Name:         "project",
			Path:         projectPath,
			LastActiveAt: time.Now().UTC(),
		}},
	})
	if err != nil {
		t.Fatalf("save project: %v", err)
	}
	writer := New(state)
	event := protocol.RunEvent{
		EventID:   "evt_new",
		ProjectID: "project_1",
		SessionID: "session_new",
		RunID:     "run_1",
		Kind:      "user.message",
		Payload:   protocol.Raw(map[string]string{"text": "new session prompt"}),
		At:        time.Now().UTC(),
	}
	if err := writer.SyncRunEvent(ctx, event); err != nil {
		t.Fatalf("sync new session event: %v", err)
	}
	sessionPath, err := state.SessionPath(ctx, "session_new")
	if err != nil {
		t.Fatalf("session path: %v", err)
	}
	if filepath.Dir(sessionPath) != filepath.Join(root, "sessions") {
		t.Fatalf("session path = %q", sessionPath)
	}
	session, err := state.Session(ctx, "session_new")
	if err != nil {
		t.Fatalf("session record: %v", err)
	}
	if session.CWD != projectPath {
		t.Fatalf("session cwd = %q, want %q", session.CWD, projectPath)
	}
	assertSessionMetaCWD(t, sessionPath, projectPath)
	messages, err := codexscan.ReadSessionMessages(ctx, sessionPath, 10, 64<<10)
	if err != nil {
		t.Fatalf("read new session messages: %v", err)
	}
	if len(messages) != 1 || messages[0].Text != "new session prompt" {
		t.Fatalf("messages = %+v", messages)
	}
}

func assertSessionMetaCWD(t *testing.T, path string, want string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session file: %v", err)
	}
	firstLine := raw
	if index := bytes.IndexByte(raw, '\n'); index >= 0 {
		firstLine = raw[:index]
	}
	var row struct {
		Type    string `json:"type"`
		Payload struct {
			CWD string `json:"cwd"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(firstLine, &row); err != nil {
		t.Fatalf("decode session meta: %v", err)
	}
	if row.Type != "session_meta" || row.Payload.CWD != want {
		t.Fatalf("session meta = %+v, want cwd %q", row, want)
	}
}
