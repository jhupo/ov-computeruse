package workspace

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ov-computeruse/agent/internal/protocol"
)

const (
	gitStatusTimeout    = 5 * time.Second
	gitDiffTimeout      = 5 * time.Second
	defaultStatusLimit  = 500
	maxStatusLimit      = 2000
	defaultDiffMaxBytes = 512 << 10
	maxDiffMaxBytes     = 2 << 20
)

type WorkspaceError struct {
	Code    string
	Message string
}

func (e WorkspaceError) Error() string {
	return e.Message
}

type GitStatus struct{}

func GitAvailable() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

func (GitStatus) Status(ctx context.Context, target Target) (protocol.WorkspaceGit, error) {
	if target.Root == "" {
		return protocol.WorkspaceGit{}, errors.New("project root is required")
	}
	runCtx, cancel := context.WithTimeout(ctx, gitStatusTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", "-C", target.Root, "status", "--porcelain=v2", "--branch", "-z")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	raw, err := cmd.Output()
	if runCtx.Err() != nil {
		return protocol.WorkspaceGit{}, workspaceErr("timeout", runCtx.Err().Error())
	}
	if err != nil {
		return protocol.WorkspaceGit{}, gitCommandError(err, stderr.String())
	}
	return parseGitStatus(string(raw)), nil
}

func (GitStatus) Diff(ctx context.Context, target Target, req protocol.WorkspaceRequest) (protocol.WorkspaceGitDiff, error) {
	if target.Root == "" {
		return protocol.WorkspaceGitDiff{}, errors.New("project root is required")
	}
	args := []string{"-C", target.Root, "diff", "--no-ext-diff", "--binary"}
	if req.Staged {
		args = append(args, "--cached")
	}
	if strings.TrimSpace(target.Rel) != "" {
		args = append(args, "--", target.Rel)
	}
	runCtx, cancel := context.WithTimeout(ctx, gitDiffTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, "git", args...)
	var stdout limitedBuffer
	stdout.max = req.MaxBytes
	if stdout.max <= 0 {
		stdout.max = defaultDiffMaxBytes
	}
	if stdout.max > maxDiffMaxBytes {
		stdout.max = maxDiffMaxBytes
	}
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if runCtx.Err() != nil {
		return protocol.WorkspaceGitDiff{}, workspaceErr("timeout", runCtx.Err().Error())
	}
	if err != nil {
		return protocol.WorkspaceGitDiff{}, gitCommandError(err, stderr.String())
	}
	data := stdout.buf.Bytes()
	diff := protocol.WorkspaceGitDiff{
		Path:      target.Rel,
		Staged:    req.Staged,
		Encoding:  "utf-8",
		Size:      stdout.size,
		Content:   string(data),
		Truncated: stdout.truncated,
	}
	if (DefaultPolicy{}).Binary(data) {
		diff.Encoding = "binary"
		diff.Binary = true
		diff.Content = ""
	}
	return diff, nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	max       int64
	size      int64
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.size += int64(len(p))
	remaining := b.max - int64(b.buf.Len())
	if remaining <= 0 {
		b.truncated = b.truncated || len(p) > 0
		return len(p), nil
	}
	if int64(len(p)) > remaining {
		_, _ = b.buf.Write(p[:int(remaining)])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

func parseGitStatus(raw string) protocol.WorkspaceGit {
	status := protocol.WorkspaceGit{Clean: true}
	records := gitStatusRecords(raw)
	for i := 0; i < len(records); i++ {
		line := records[i]
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "# ") {
			parseGitHeader(&status, line)
			continue
		}
		var next string
		if line[0] == '2' && i+1 < len(records) {
			next = records[i+1]
			i++
		}
		change, ok := parseGitChange(line, next)
		if !ok {
			continue
		}
		status.Files = append(status.Files, change)
		countGitChange(&status.Counts, change)
	}
	status.Counts.Total = len(status.Files)
	status.Clean = status.Counts.Total == 0
	return status
}

func (GitStatus) Limit(status protocol.WorkspaceGit, limit int) protocol.WorkspaceGit {
	limit = clamp(limit, defaultStatusLimit, maxStatusLimit)
	if len(status.Files) <= limit {
		return status
	}
	status.Files = status.Files[:limit]
	status.Truncated = true
	return status
}

func gitStatusRecords(raw string) []string {
	if strings.Contains(raw, "\x00") {
		return strings.Split(raw, "\x00")
	}
	return strings.Split(raw, "\n")
}

func parseGitHeader(status *protocol.WorkspaceGit, line string) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return
	}
	switch fields[1] {
	case "branch.oid":
		if fields[2] != "(initial)" {
			status.Head = fields[2]
		}
	case "branch.head":
		if fields[2] != "(detached)" {
			status.Branch = fields[2]
		}
	case "branch.upstream":
		status.Upstream = fields[2]
	case "branch.ab":
		for _, value := range fields[2:] {
			if strings.HasPrefix(value, "+") {
				status.Ahead, _ = strconv.Atoi(strings.TrimPrefix(value, "+"))
			}
			if strings.HasPrefix(value, "-") {
				status.Behind, _ = strconv.Atoi(strings.TrimPrefix(value, "-"))
			}
		}
	}
}

func parseGitChange(line, next string) (protocol.WorkspaceGitChange, bool) {
	switch line[0] {
	case '1':
		return parseOrdinaryChange(line)
	case '2':
		return parseRenameChange(line, next)
	case 'u':
		return parseUnmergedChange(line)
	case '?':
		path := strings.TrimPrefix(line, "? ")
		return protocol.WorkspaceGitChange{Path: path, Worktree: "?", Kind: "untracked"}, path != ""
	default:
		return protocol.WorkspaceGitChange{}, false
	}
}

func parseOrdinaryChange(line string) (protocol.WorkspaceGitChange, bool) {
	fields := strings.SplitN(line, " ", 9)
	if len(fields) < 9 {
		return protocol.WorkspaceGitChange{}, false
	}
	path := fields[8]
	index, worktree := splitXY(fields[1])
	return protocol.WorkspaceGitChange{Path: path, Index: index, Worktree: worktree, Kind: gitKind(index, worktree)}, path != ""
}

func parseRenameChange(line, next string) (protocol.WorkspaceGitChange, bool) {
	fields := strings.SplitN(line, " ", 10)
	if len(fields) < 10 {
		return protocol.WorkspaceGitChange{}, false
	}
	paths := fields[9]
	path := paths
	oldPath := next
	if oldPath == "" {
		if left, right, ok := strings.Cut(paths, "\t"); ok {
			path = left
			oldPath = right
		}
	}
	index, worktree := splitXY(fields[1])
	return protocol.WorkspaceGitChange{Path: path, OldPath: oldPath, Index: index, Worktree: worktree, Kind: "renamed"}, path != ""
}

func parseUnmergedChange(line string) (protocol.WorkspaceGitChange, bool) {
	fields := strings.SplitN(line, " ", 11)
	if len(fields) < 11 {
		return protocol.WorkspaceGitChange{}, false
	}
	path := fields[10]
	index, worktree := splitXY(fields[1])
	return protocol.WorkspaceGitChange{Path: path, Index: index, Worktree: worktree, Kind: "conflicted", Conflicted: true}, path != ""
}

func splitXY(value string) (string, string) {
	if len(value) < 2 {
		return "", ""
	}
	return value[:1], value[1:2]
}

func gitKind(index, worktree string) string {
	switch {
	case index == "A" || worktree == "A":
		return "added"
	case index == "D" || worktree == "D":
		return "deleted"
	case index == "R" || worktree == "R":
		return "renamed"
	default:
		return "modified"
	}
}

func countGitChange(counts *protocol.WorkspaceGitCounts, change protocol.WorkspaceGitChange) {
	switch change.Kind {
	case "added":
		counts.Added++
	case "deleted":
		counts.Deleted++
	case "renamed":
		counts.Renamed++
	case "untracked":
		counts.Untracked++
	case "conflicted":
		counts.Conflicted++
	default:
		counts.Modified++
	}
}

func gitCommandError(err error, stderr string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return workspaceErr("git_unavailable", "git executable is not available")
	}
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not a git repository"):
		return workspaceErr("not_git_repo", msg)
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "access is denied"):
		return workspaceErr("permission_denied", msg)
	default:
		return workspaceErr("git_failed", msg)
	}
}

func workspaceErr(code, message string) WorkspaceError {
	return WorkspaceError{Code: code, Message: message}
}
