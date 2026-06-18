package transport

import (
	"testing"
	"time"

	"ov-computeruse/agent/internal/protocol"
)

func TestUniqueRuntimeSessionsKeepsNativeOnlySessions(t *testing.T) {
	oldAt := time.Now().UTC().Add(-time.Minute)
	newAt := time.Now().UTC()
	sessions := uniqueRuntimeSessions([]protocol.RuntimeSession{
		{Runtime: "codex.native", NativeSessionID: "native_1", LastResponseID: "resp_old", UpdatedAt: oldAt},
		{Runtime: "codex.native", NativeSessionID: "native_1", LastResponseID: "resp_new", UpdatedAt: newAt},
		{Runtime: "openai.responses", SessionID: "session_1", NativeSessionID: "responses:resp_1", LastResponseID: "resp_1", UpdatedAt: newAt},
	})
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2: %+v", len(sessions), sessions)
	}
	foundNative := false
	for _, session := range sessions {
		if session.Runtime == "codex.native" {
			foundNative = true
			if session.NativeSessionID != "native_1" || session.LastResponseID != "resp_new" {
				t.Fatalf("native session = %+v", session)
			}
		}
	}
	if !foundNative {
		t.Fatalf("native-only runtime session was dropped: %+v", sessions)
	}
}
