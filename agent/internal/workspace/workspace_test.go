package workspace

import (
	"context"
	"database/sql"
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
