package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ov-computeruse/agent/internal/protocol"
)

type fakeProjectSource struct {
	projects []Project
}

func (s fakeProjectSource) Projects(context.Context) ([]Project, error) {
	return s.projects, nil
}

type fakeGitUpdateSink struct {
	updates []protocol.WorkspaceGitUpdated
}

func (s *fakeGitUpdateSink) WorkspaceGitUpdated(_ context.Context, update protocol.WorkspaceGitUpdated) error {
	s.updates = append(s.updates, update)
	return nil
}

func TestNotifierPublishesOnlyChangedGitStatus(t *testing.T) {
	root := t.TempDir()
	runNotifierGit(t, root, "init")
	runNotifierGit(t, root, "config", "user.email", "test@example.com")
	runNotifierGit(t, root, "config", "user.name", "Test User")
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runNotifierGit(t, root, "add", "main.go")
	runNotifierGit(t, root, "commit", "-m", "init")

	sink := &fakeGitUpdateSink{}
	notifier := &Notifier{
		Projects: fakeProjectSource{projects: []Project{{ID: "project_1", Path: root}}},
		Sink:     sink,
		Git:      GitStatus{},
	}
	if err := notifier.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := notifier.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.updates) != 1 {
		t.Fatalf("updates after unchanged publish = %d", len(sink.updates))
	}
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := notifier.publish(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.updates) != 2 {
		t.Fatalf("updates after changed publish = %d", len(sink.updates))
	}
	if sink.updates[1].Status != "ok" || sink.updates[1].Git == nil || sink.updates[1].Git.Clean {
		t.Fatalf("changed update = %+v", sink.updates[1])
	}
}

func runNotifierGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, raw)
	}
}
