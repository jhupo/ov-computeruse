package store

import (
	"strings"
	"testing"
	"time"

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

func TestSessionTargetQueryAcceptsHistoryOnlySessions(t *testing.T) {
	query := sessionTargetQuery()
	for _, want := range []string{"FROM history_items", "session_id AS id", "4 AS priority"} {
		if !strings.Contains(query, want) {
			t.Fatalf("session target query missing %q:\n%s", want, query)
		}
	}
}

func TestSessionExistsQueryAcceptsHistoryOnlySessions(t *testing.T) {
	query := sessionExistsQuery()
	if !strings.Contains(query, "EXISTS(SELECT 1 FROM history_items WHERE agent_id=$1 AND session_id=$2)") {
		t.Fatalf("session exists query does not accept history-only sessions:\n%s", query)
	}
}

func TestRuntimeTimelineFromEventPreservesCodexSemantics(t *testing.T) {
	event := protocol.RunEvent{
		RunID:     "run_1",
		SessionID: "session_1",
		ProjectID: "project_1",
		Seq:       12,
		Kind:      "assistant.message.delta",
		Payload: protocol.Raw(map[string]string{
			"runtime":   protocol.RuntimeCodexCLI,
			"thread_id": "thread_1",
			"turn_id":   "turn_1",
			"item_id":   "item_1",
			"item_type": "agent_message",
			"phase":     "item.updated",
			"text":      "hello",
		}),
		At: time.Now().UTC(),
	}
	item := runtimeTimelineFromEvent("agent_1", event)
	if item.Runtime != protocol.RuntimeCodexCLI || item.ThreadID != "thread_1" || item.TurnID != "turn_1" || item.ItemID != "item_1" {
		t.Fatalf("runtime timeline identity not preserved: %+v", item)
	}
	if item.Role != "assistant" || item.Text != "hello" || item.ItemType != "agent_message" || item.Phase != "item.updated" {
		t.Fatalf("runtime timeline content not preserved: %+v", item)
	}
}

func TestRuntimeTimelineSkipsNonRuntimeEvents(t *testing.T) {
	item := runtimeTimelineFromEvent("agent_1", protocol.RunEvent{
		RunID: "run_1",
		Seq:   1,
		Kind:  "run.started",
		At:    time.Now().UTC(),
	})
	if item.Runtime != "" {
		t.Fatalf("non-runtime event projected unexpectedly: %+v", item)
	}
}

func TestRuntimeTimelineSessionQueryAcceptsSessionOrThreadID(t *testing.T) {
	query := runtimeTimelineSessionQuery()
	for _, want := range []string{
		"session_id=$2 OR thread_id=$2",
		"history AS",
		"FROM history_items hi",
		"LEFT JOIN LATERAL",
		"LIMIT 1",
		"live.item_id=COALESCE(NULLIF(hi.source_event_id, ''), NULLIF(hi.payload->>'id', ''), NULLIF(hi.payload->>'call_id', ''), hi.id)",
		"COALESCE(hi.payload->>'turn_id', '') AS turn_id",
		"NULLIF(hi.payload->>'id', '')",
		"WHEN hi.kind='tool.call' THEN COALESCE(NULLIF(hi.payload->>'type', '')",
		"COALESCE(hi.payload->>'status', '') AS status",
		"WHEN hi.kind='message' AND COALESCE(hi.role, '')='user' THEN 'user.message'",
		"WHEN hi.kind='todo.list' THEN 'todo_list'",
		"combined AS",
		"recent AS",
		"ORDER BY event_at DESC, received_at DESC, run_id DESC, seq DESC",
		"FROM recent",
		"ORDER BY event_at ASC",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("runtime timeline session query missing %q:\n%s", want, query)
		}
	}
	if strings.Contains(query, "AND NOT EXISTS (SELECT 1 FROM live)") {
		t.Fatalf("runtime timeline session query hides all history when live events exist:\n%s", query)
	}
}
