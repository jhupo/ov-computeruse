package app

import (
	"testing"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

func TestValidateCommandRejectsNewSessionWithSessionID(t *testing.T) {
	command := protocol.Command{
		Kind:      "command.new_session",
		ProjectID: "project_1",
		SessionID: "foreign_session",
		Payload:   protocol.Raw(map[string]string{"prompt": "hi"}),
	}
	if err := validateCommand(command); err == nil {
		t.Fatal("expected new_session with session_id to be rejected")
	}
}

func TestValidateCommandRejectsNewSessionWithoutProject(t *testing.T) {
	command := protocol.Command{
		Kind:    "command.new_session",
		Payload: protocol.Raw(map[string]string{"prompt": "hi"}),
	}
	if err := validateCommand(command); err == nil {
		t.Fatal("expected new_session without project_id to be rejected")
	}
}

func TestEnsureCommandProjectMatchesSessionRejectsCrossProject(t *testing.T) {
	err := ensureCommandProjectMatchesSession("project_a", store.SessionTarget{ID: "session_1", ProjectID: "project_b"})
	if err == nil {
		t.Fatal("expected project/session mismatch to be rejected")
	}
}

func TestEnsureStopTargetsMatchRejectsCrossSessionRun(t *testing.T) {
	err := ensureStopTargetsMatch("", "session_a", store.RunTarget{ID: "run_1", SessionID: "session_b"}, store.SessionTarget{ID: "session_a"})
	if err == nil {
		t.Fatal("expected run/session mismatch to be rejected")
	}
}

func TestEnsureApprovalTargetsMatchRejectsCrossProjectRun(t *testing.T) {
	err := ensureApprovalTargetsMatch("project_a", "", store.RunTarget{ID: "run_1", ProjectID: "project_b"}, store.SessionTarget{})
	if err == nil {
		t.Fatal("expected approval run/project mismatch to be rejected")
	}
}

func TestValidateCommandCapabilitiesRejectsApprovalDecisionWithoutFeature(t *testing.T) {
	identity := store.AgentIdentity{
		Capabilities: protocol.Raw(protocol.Capabilities{
			SupportsRuntime: true,
			Features:        []string{"command.approval_decision", "command.new_session", "runtime." + protocol.RuntimeCodexCLI},
		}),
	}
	command := protocol.Command{
		Kind:    "command.approval_decision",
		RunID:   "run_1",
		Payload: protocol.Raw(protocol.ApprovalDecision{ApprovalID: "approval_1", Decision: "approved"}),
	}
	if err := validateCommandCapabilities(identity, command); err == nil {
		t.Fatal("expected approval decision to require explicit approval.decision feature")
	}
}

func TestValidateCommandCapabilitiesAcceptsRuntimeCommand(t *testing.T) {
	identity := store.AgentIdentity{
		Capabilities: protocol.Raw(protocol.Capabilities{
			SupportsRuntime: true,
			Features:        []string{"command.new_session", "runtime." + protocol.RuntimeCodexCLI},
		}),
	}
	command := protocol.Command{
		Kind:    "command.new_session",
		Payload: protocol.Raw(map[string]string{"prompt": "hi"}),
	}
	if err := validateCommandCapabilities(identity, command); err != nil {
		t.Fatalf("runtime command rejected: %v", err)
	}
}
