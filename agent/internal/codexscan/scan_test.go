package codexscan

import (
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
