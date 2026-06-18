package app

import (
	"testing"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

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
