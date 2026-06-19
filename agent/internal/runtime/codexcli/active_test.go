package codexcli

import (
	"context"
	"testing"

	"ov-computeruse/agent/internal/protocol"
)

func TestActiveRunsCancelByAliasedNativeSession(t *testing.T) {
	var active activeRuns
	cancelled := false
	active.track(protocol.Command{RunID: "run_1", SessionID: "display_session"}, func() {
		cancelled = true
	})
	active.alias("run_1", "native_thread")

	cancel, ok := active.cancel(protocol.Command{SessionID: "native_thread"})
	if !ok {
		t.Fatal("expected native session alias to resolve active run")
	}
	cancel()
	if !cancelled {
		t.Fatal("expected aliased cancel to invoke active run cancel")
	}
	active.untrack(protocol.Command{RunID: "run_1", SessionID: "display_session"})
	if _, ok := active.cancel(protocol.Command{SessionID: "native_thread"}); ok {
		t.Fatal("expected native session alias to be removed after untrack")
	}
}

func TestActiveRunsAliasIgnoresUnknownRun(t *testing.T) {
	var active activeRuns
	active.alias("missing_run", "native_thread")
	if _, ok := active.cancel(protocol.Command{SessionID: "native_thread"}); ok {
		t.Fatal("unexpected cancel for unknown run alias")
	}
	active.track(protocol.Command{RunID: "run_1"}, context.CancelFunc(func() {}))
	if _, ok := active.cancel(protocol.Command{RunID: "run_1"}); !ok {
		t.Fatal("expected tracked run to remain cancellable")
	}
}
