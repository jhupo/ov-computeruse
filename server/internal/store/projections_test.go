package store

import (
	"strings"
	"testing"

	"ov-computeruse/server/internal/protocol"
)

func TestStatusStepProjectionMapsUnsupportedCodexApproval(t *testing.T) {
	event := protocol.RunEvent{
		Kind:    "run.status",
		Payload: protocol.Raw(map[string]string{"status": "codex.approval.unsupported"}),
	}
	kind, title, status := statusStepProjection(event)
	if kind != "approval" || title != "Approval unsupported" || status != "unsupported" {
		t.Fatalf("projection = %q %q %q", kind, title, status)
	}
}

func TestStatusStepProjectionIgnoresRegularStatus(t *testing.T) {
	event := protocol.RunEvent{
		Kind:    "run.status",
		Payload: protocol.Raw(map[string]string{"status": "codex.turn.started"}),
	}
	kind, title, status := statusStepProjection(event)
	if kind != "" || title != "" || status != "" {
		t.Fatalf("projection = %q %q %q, want empty", kind, title, status)
	}
}

func TestConversationQueryHidesRemoteRunsAfterHistorySync(t *testing.T) {
	query := conversationItemsQuery()
	for _, want := range []string{"r.finished_at IS NULL", "history_items hi", "hi.received_at >= r.finished_at"} {
		if !strings.Contains(query, want) {
			t.Fatalf("conversation query missing %q:\n%s", want, query)
		}
	}
}
