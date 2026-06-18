package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"ov-computeruse/agent/internal/protocol"
)

const (
	defaultNotifyInterval = 10 * time.Second
	defaultNotifyLimit    = 200
)

type Project struct {
	ID   string
	Path string
}

type ProjectSource interface {
	Projects(context.Context) ([]Project, error)
}

type GitUpdateSink interface {
	WorkspaceGitUpdated(context.Context, protocol.WorkspaceGitUpdated) error
}

type Notifier struct {
	Projects ProjectSource
	Sink     GitUpdateSink
	Git      GitStatus
	Interval time.Duration
	Limit    int
	seen     map[string]string
}

func (n *Notifier) Run(ctx context.Context) error {
	if n == nil || n.Projects == nil || n.Sink == nil {
		return nil
	}
	if !GitAvailable() {
		return nil
	}
	if n.seen == nil {
		n.seen = map[string]string{}
	}
	if n.Interval <= 0 {
		n.Interval = defaultNotifyInterval
	}
	if err := n.publish(ctx); err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	ticker := time.NewTicker(n.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			_ = n.publish(ctx)
		}
	}
}

func (n *Notifier) publish(ctx context.Context) error {
	if n.seen == nil {
		n.seen = map[string]string{}
	}
	projects, err := n.Projects.Projects(ctx)
	if err != nil {
		return err
	}
	active := map[string]struct{}{}
	for _, project := range projects {
		project.ID = strings.TrimSpace(project.ID)
		project.Path = strings.TrimSpace(project.Path)
		if project.ID == "" || project.Path == "" {
			continue
		}
		active[project.ID] = struct{}{}
		update := n.projectUpdate(ctx, project)
		fingerprint := updateFingerprint(update)
		if n.seen[project.ID] == fingerprint {
			continue
		}
		n.seen[project.ID] = fingerprint
		if err := n.Sink.WorkspaceGitUpdated(ctx, update); err != nil {
			return err
		}
	}
	for projectID := range n.seen {
		if _, ok := active[projectID]; !ok {
			delete(n.seen, projectID)
		}
	}
	return nil
}

func (n *Notifier) projectUpdate(ctx context.Context, project Project) protocol.WorkspaceGitUpdated {
	update := protocol.WorkspaceGitUpdated{ProjectID: project.ID, At: time.Now().UTC()}
	root, err := realDirectory(project.Path)
	if err != nil {
		update.Status = "failed"
		update.Code = workspaceErrorCode(err, "invalid_workspace_path")
		update.Message = err.Error()
		return update
	}
	git, err := n.Git.Status(ctx, Target{Root: root})
	if err != nil {
		update.Status = "failed"
		update.Code = workspaceErrorCode(err, "git_status_failed")
		update.Message = err.Error()
		return update
	}
	git = n.Git.Limit(git, n.Limit)
	update.Status = "ok"
	update.Git = &git
	return update
}

func updateFingerprint(update protocol.WorkspaceGitUpdated) string {
	stable := struct {
		ProjectID string                 `json:"project_id"`
		Status    string                 `json:"status"`
		Code      string                 `json:"code,omitempty"`
		Message   string                 `json:"message,omitempty"`
		Git       *protocol.WorkspaceGit `json:"git,omitempty"`
	}{
		ProjectID: update.ProjectID,
		Status:    update.Status,
		Code:      update.Code,
		Message:   normalizeUpdateMessage(update.Code, update.Message),
		Git:       update.Git,
	}
	raw, err := json.Marshal(stable)
	if err != nil {
		return update.ProjectID + "\x00" + update.Status + "\x00" + update.Code + "\x00" + update.Message
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func normalizeUpdateMessage(code, message string) string {
	if strings.TrimSpace(code) == "" {
		return strings.TrimSpace(message)
	}
	switch strings.TrimSpace(code) {
	case "timeout":
		return "timeout"
	default:
		return strings.TrimSpace(message)
	}
}
