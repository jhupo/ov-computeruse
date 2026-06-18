package codexscan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResponseIDFromPayload(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "direct response id",
			raw:  `{"response_id":"resp_direct"}`,
			want: "resp_direct",
		},
		{
			name: "nested response id",
			raw:  `{"response":{"id":"resp_nested"}}`,
			want: "resp_nested",
		},
		{
			name: "event item response id",
			raw:  `{"event":{"item":{"response":{"id":"resp_event_item"}}}}`,
			want: "resp_event_item",
		},
		{
			name: "array chooses newest nested id",
			raw:  `{"data":[{"response_id":"resp_old"},{"response_id":"resp_new"}]}`,
			want: "resp_new",
		},
		{
			name: "camel case last response id",
			raw:  `{"lastResponseId":"resp_camel"}`,
			want: "resp_camel",
		},
		{
			name: "raw json string response id",
			raw:  `{"raw":"{\"response\":{\"id\":\"resp_raw\"}}"}`,
			want: "resp_raw",
		},
		{
			name: "previous response id is ignored",
			raw:  `{"previous_response_id":"resp_previous"}`,
			want: "",
		},
		{
			name: "non response id is ignored",
			raw:  `{"id":"call_123"}`,
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := responseIDFromPayload([]byte(tt.raw)); got != tt.want {
				t.Fatalf("responseIDFromPayload() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRuntimeSessionFromFileUsesLastResponseID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	content := "" +
		`{"timestamp":"2026-06-18T01:00:00Z","type":"session_meta","payload":{"id":"sess_native"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:01:00Z","type":"event_msg","payload":{"response":{"id":"resp_old"}}}` + "\n" +
		`{"timestamp":"2026-06-18T01:02:00Z","type":"event_msg","payload":{"event":{"item":{"response":{"id":"resp_new"}}}}}` + "\n"
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
	if runtimeSession.NativeSessionID != "sess_native" {
		t.Fatalf("native session id = %q, want sess_native", runtimeSession.NativeSessionID)
	}
	if runtimeSession.LastResponseID != "resp_new" {
		t.Fatalf("last response id = %q, want resp_new", runtimeSession.LastResponseID)
	}
	if runtimeSession.SessionID != "sess_local" {
		t.Fatalf("session id = %q, want sess_local", runtimeSession.SessionID)
	}
}

func TestRuntimeSessionFromFileReadsFlatLegacyRows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.jsonl")
	content := "" +
		`{"timestamp":"2026-06-18T01:00:00Z","type":"session_meta","payload":{"id":"sess_native"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:01:00Z","response":{"id":"resp_flat"}}` + "\n" +
		`{"timestamp":"2026-06-18T01:02:00Z","lastResponseId":"resp_flat_new"}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeSession := runtimeSessionFromFile(Session{
		ID:        "sess_local",
		Path:      path,
		UpdatedAt: time.Date(2026, 6, 18, 1, 0, 0, 0, time.UTC),
	})
	if runtimeSession.LastResponseID != "resp_flat_new" {
		t.Fatalf("last response id = %q, want resp_flat_new", runtimeSession.LastResponseID)
	}
}
