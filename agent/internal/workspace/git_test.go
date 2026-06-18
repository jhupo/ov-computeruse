package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"ov-computeruse/agent/internal/protocol"
)

func TestParseGitStatusPorcelainV2(t *testing.T) {
	status := parseGitStatus("# branch.oid abc123\x00" +
		"# branch.head main\x00" +
		"# branch.upstream origin/main\x00" +
		"# branch.ab +2 -1\x00" +
		"1 .M N... 100644 100644 100644 abc abc file.go\x00" +
		"1 A. N... 000000 100644 100644 000 abc added.go\x00" +
		"2 R. N... 100644 100644 100644 abc def R100 new.go\x00old.go\x00" +
		"? scratch.txt\x00" +
		"u UU N... 100644 100644 100644 100644 a b c d conflict.go\x00")
	if status.Branch != "main" || status.Head != "abc123" || status.Upstream != "origin/main" || status.Ahead != 2 || status.Behind != 1 {
		t.Fatalf("branch metadata = %+v", status)
	}
	if status.Clean {
		t.Fatal("status should be dirty")
	}
	if status.Counts.Total != 5 || status.Counts.Modified != 1 || status.Counts.Added != 1 || status.Counts.Renamed != 1 || status.Counts.Untracked != 1 || status.Counts.Conflicted != 1 {
		t.Fatalf("counts = %+v", status.Counts)
	}
	if status.Files[2].Kind != "renamed" || status.Files[2].Path != "new.go" || status.Files[2].OldPath != "old.go" {
		t.Fatalf("rename = %+v", status.Files[2])
	}
}

func TestGitStatusLimitMarksTruncated(t *testing.T) {
	status := protocol.WorkspaceGit{
		Files: []protocol.WorkspaceGitChange{
			{Path: "one.go", Kind: "modified"},
			{Path: "two.go", Kind: "modified"},
		},
	}
	status = (GitStatus{}).Limit(status, 1)
	if len(status.Files) != 1 || !status.Truncated {
		t.Fatalf("limited status = %+v", status)
	}
}

func TestGitDiffReadsWorkingTreeDiff(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test User")
	path := filepath.Join(root, "main.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "main.go")
	runGit(t, root, "commit", "-m", "init")
	if err := os.WriteFile(path, []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	diff, err := (GitStatus{}).Diff(context.Background(), Target{Root: root, Path: path, Rel: "main.go"}, protocol.WorkspaceRequest{Path: "main.go"})
	if err != nil {
		t.Fatal(err)
	}
	if diff.Path != "main.go" || diff.Encoding != "utf-8" || diff.Content == "" || diff.Truncated {
		t.Fatalf("diff = %+v", diff)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, raw)
	}
}
