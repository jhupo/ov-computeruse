package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ov-computeruse/agent/internal/protocol"
)

type fakeState struct {
	projects map[string]string
}

func (s fakeState) ProjectPath(context.Context, string) (string, error) {
	path := s.projects["project_1"]
	if path == "" {
		return "", sql.ErrNoRows
	}
	return path, nil
}

func TestWorkspaceErrorCodeMapsContextCancellationToTimeout(t *testing.T) {
	if got := workspaceErrorCode(context.DeadlineExceeded, "fallback"); got != "timeout" {
		t.Fatalf("deadline code = %q, want timeout", got)
	}
	if got := workspaceErrorCode(context.Canceled, "fallback"); got != "timeout" {
		t.Fatalf("canceled code = %q, want timeout", got)
	}
	if got := workspaceErrorCode(errors.New("other"), "fallback"); got != "fallback" {
		t.Fatalf("fallback code = %q, want fallback", got)
	}
}

func TestHandlerListsAndReadsProjectFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	list := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "list", ProjectID: "project_1"})
	if list.Status != "ok" || len(list.Entries) != 1 || list.Entries[0].Path != "main.go" {
		t.Fatalf("list response = %+v", list)
	}
	read := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_2", Operation: "read", ProjectID: "project_1", Path: "main.go"})
	if read.Status != "ok" || read.File == nil || read.File.Content != "package main\n" {
		t.Fatalf("read response = %+v", read)
	}
}

func TestHandlerSearchesProjectFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "app", "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden", "main_secret.go"), []byte("package hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "main.js"), []byte("console.log('skip')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	resp := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "search", ProjectID: "project_1", Query: "main", Depth: 4})
	if resp.Status != "ok" || len(resp.Matches) == 0 {
		t.Fatalf("search response = %+v", resp)
	}
	if resp.Matches[0].Path != "cmd/app/main.go" {
		t.Fatalf("first match = %+v", resp.Matches[0])
	}
	for _, match := range resp.Matches {
		if match.Path == ".hidden/main_secret.go" {
			t.Fatalf("hidden match leaked = %+v", match)
		}
		if match.Path == "node_modules/pkg/main.js" {
			t.Fatalf("skipped directory match leaked = %+v", match)
		}
	}
}

func TestHandlerSearchesProjectFileContent(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "app"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "app", "service.go"), []byte("package app\n\nfunc Run() string {\n\treturn \"needle-value\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("NEEDLE_SECRET=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	resp := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "search", ProjectID: "project_1", Query: "needle-value", Depth: 4})
	if resp.Status != "ok" || len(resp.Matches) != 1 {
		t.Fatalf("search response = %+v", resp)
	}
	match := resp.Matches[0]
	if match.Path != "internal/app/service.go" || match.Line != 4 || match.Preview != `return "needle-value"` {
		t.Fatalf("content match = %+v", match)
	}
}

func TestHandlerRejectsEscapedPath(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	resp := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "read", ProjectID: "project_1", Path: "../secret.txt"})
	if resp.Status != "failed" && resp.Status != "rejected" {
		t.Fatalf("escaped path status = %q", resp.Status)
	}
}

func TestHandlerRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "outside")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	resp := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "read", ProjectID: "project_1", Path: "outside/secret.txt"})
	if resp.Status != "failed" && resp.Status != "rejected" {
		t.Fatalf("symlink escape status = %q", resp.Status)
	}
}

func TestHandlerRejectsSensitiveFileRead(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("TOKEN=value"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	resp := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "read", ProjectID: "project_1", Path: ".env"})
	if resp.Status != "failed" {
		t.Fatalf("sensitive read status = %q", resp.Status)
	}
}

func TestHandlerDoesNotEnumerateSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".env", "api_token.txt", "client_secret.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})

	list := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_list", Operation: "list", ProjectID: "project_1", IncludeHidden: true})
	if list.Status != "ok" {
		t.Fatalf("list status = %q", list.Status)
	}
	for _, entry := range list.Entries {
		if entry.Path == ".env" || entry.Path == "api_token.txt" || entry.Path == "client_secret.json" {
			t.Fatalf("sensitive file was listed: %+v", entry)
		}
	}

	search := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_search", Operation: "search", ProjectID: "project_1", Query: "secret", IncludeHidden: true})
	if search.Status != "ok" {
		t.Fatalf("search status = %q", search.Status)
	}
	if len(search.Matches) != 0 {
		t.Fatalf("sensitive file was searchable: %+v", search.Matches)
	}
}

func TestHandlerMarksLimitedWorkspaceResultsPartial(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	handler := New(fakeState{projects: map[string]string{"project_1": root}})
	resp := handler.Handle(context.Background(), protocol.WorkspaceRequest{RequestID: "req_1", Operation: "list", ProjectID: "project_1", Limit: 1})
	if resp.Status != "ok" {
		t.Fatalf("list status = %q", resp.Status)
	}
	if !resp.Partial || len(resp.Warnings) == 0 {
		t.Fatalf("expected partial limited response, got partial=%v warnings=%v", resp.Partial, resp.Warnings)
	}
}
