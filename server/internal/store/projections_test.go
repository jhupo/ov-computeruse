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

func TestListSessionsQueryIncludesHistoryOnlyFallback(t *testing.T) {
	query, args := listSessionsQuery("agent_1", "", 200)
	for _, want := range []string{"history_only AS", "'history_items' AS id_source", "FROM history_only"} {
		if !strings.Contains(query, want) {
			t.Fatalf("session query missing %q:\n%s", want, query)
		}
	}
	if len(args) != 2 || args[0] != "agent_1" || args[1] != 200 {
		t.Fatalf("args = %#v", args)
	}
}

func TestListSessionsQueryOmitsHistoryOnlyFallbackWhenProjectFiltered(t *testing.T) {
	query, args := listSessionsQuery("agent_1", "project_1", 50)
	if strings.Contains(query, "history_only AS") || strings.Contains(query, "FROM history_only") {
		t.Fatalf("project-filtered session query should not include history-only sessions:\n%s", query)
	}
	if len(args) != 3 || args[0] != "agent_1" || args[1] != "project_1" || args[2] != 50 {
		t.Fatalf("args = %#v", args)
	}
}
