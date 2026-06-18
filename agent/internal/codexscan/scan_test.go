package codexscan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ov-computeruse/agent/internal/protocol"
)

func TestRuntimeSessionFromFileUsesCodexSessionMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := "" +
		`{"timestamp":"2026-06-18T01:00:00Z","type":"session_meta","payload":{"id":"sess_native"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:02:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	session := Session{
		ID:        "sess_local",
		ProjectID: "project_1",
		Path:      path,
		UpdatedAt: time.Date(2026, 6, 18, 1, 0, 0, 0, time.UTC),
	}
	runtimeSession := runtimeSessionFromFile(session)
	if runtimeSession.Runtime != protocol.RuntimeCodexCLI {
		t.Fatalf("runtime = %q, want %q", runtimeSession.Runtime, protocol.RuntimeCodexCLI)
	}
	if runtimeSession.NativeSessionID != "sess_native" {
		t.Fatalf("native session id = %q, want sess_native", runtimeSession.NativeSessionID)
	}
	if runtimeSession.SessionID != "sess_local" {
		t.Fatalf("session id = %q, want sess_local", runtimeSession.SessionID)
	}
	if runtimeSession.ResumeMode != "codex_cli_history_index" {
		t.Fatalf("resume mode = %q, want codex_cli_history_index", runtimeSession.ResumeMode)
	}
}

func TestRuntimeSessionFromFileFallsBackToIndexedSessionID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := `{"timestamp":"2026-06-18T01:02:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeSession := runtimeSessionFromFile(Session{
		ID:        "sess_local",
		Path:      path,
		UpdatedAt: time.Date(2026, 6, 18, 1, 0, 0, 0, time.UTC),
	})
	if runtimeSession.NativeSessionID != "sess_local" {
		t.Fatalf("native session id = %q, want sess_local", runtimeSession.NativeSessionID)
	}
}

func TestReadSessionItemsParsesCodexCLIItemTypes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := "" +
		`{"timestamp":"2026-06-18T01:00:00Z","type":"response_item","payload":{"type":"agent_message","text":"hello from codex"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:01:00Z","type":"response_item","payload":{"type":"command_execution","command":"git status","aggregated_output":"clean"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:02:00Z","type":"response_item","payload":{"type":"mcp_tool_call","server":"fs","tool":"read","arguments":{"path":"README.md"},"result":{"content":"ok"}}}` + "\n" +
		`{"timestamp":"2026-06-18T01:03:00Z","type":"response_item","payload":{"type":"file_change","changes":[{"path":"a.go","kind":"modified"}]}}` + "\n" +
		`{"timestamp":"2026-06-18T01:04:00Z","type":"response_item","payload":{"type":"todo_list","items":[{"text":"ship","completed":false}]}}` + "\n" +
		`{"timestamp":"2026-06-18T01:05:00Z","type":"response_item","payload":{"type":"exec_approval_request","command":"git push","cwd":"C:\\repo"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:06:00Z","type":"response_item","payload":{"type":"error","message":"failed"}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := ReadSessionItems(context.Background(), Session{ID: "session_1", Path: path}, 100, 2<<20)
	if err != nil {
		t.Fatalf("read session items: %v", err)
	}
	if len(items) != 7 {
		t.Fatalf("item count = %d, want 7: %+v", len(items), items)
	}
	want := []struct {
		kind string
		text string
	}{
		{"message", "hello from codex"},
		{"tool.call", "git status"},
		{"tool.call", "read"},
		{"tool.call", "a.go"},
		{"todo.list", "ship"},
		{"approval.requested", "git push"},
		{"error", "failed"},
	}
	for i, expected := range want {
		if items[i].Kind != expected.kind {
			t.Fatalf("item %d kind = %q, want %q", i, items[i].Kind, expected.kind)
		}
		if items[i].Text != expected.text {
			t.Fatalf("item %d text = %q, want %q", i, items[i].Text, expected.text)
		}
	}
}
