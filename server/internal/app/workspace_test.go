package app

import (
	"testing"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

func TestNormalizeWorkspaceRequestFillsIDAndTrimsFields(t *testing.T) {
	req := normalizeWorkspaceRequest(protocol.WorkspaceRequest{
		Operation: " read ",
		ProjectID: " project_1 ",
		Path:      " internal/app.go ",
		Query:     " needle ",
	})
	if req.RequestID == "" {
		t.Fatal("request id was not generated")
	}
	if req.Operation != "read" || req.ProjectID != "project_1" || req.Path != "internal/app.go" || req.Query != "needle" {
		t.Fatalf("request was not normalized: %+v", req)
	}
}

func TestValidateWorkspaceCapabilityAcceptsFileOperations(t *testing.T) {
	identity := store.AgentIdentity{
		Capabilities: protocol.Raw(protocol.Capabilities{Features: []string{"workspace.files"}}),
	}
	for _, operation := range []string{"list", "read", "search"} {
		if err := validateWorkspaceCapability(identity, operation); err != nil {
			t.Fatalf("%s rejected: %v", operation, err)
		}
	}
}

func TestValidateWorkspaceCapabilityRequiresGitFeatures(t *testing.T) {
	identity := store.AgentIdentity{
		Capabilities: protocol.Raw(protocol.Capabilities{Features: []string{"workspace.files"}}),
	}
	if err := validateWorkspaceCapability(identity, "git_status"); err == nil {
		t.Fatal("expected git_status to require git capability")
	}
}
