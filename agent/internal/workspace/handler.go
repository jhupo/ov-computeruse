package workspace

import (
	"context"
	"errors"
	"strings"
	"time"

	"ov-computeruse/agent/internal/protocol"
)

const workspaceFeatureName = "workspace.files"

type State interface {
	ProjectPath(context.Context, string) (string, error)
}

type Handler struct {
	resolver Resolver
	fs       Filesystem
	git      GitStatus
}

func New(state State) Handler {
	return Handler{resolver: NewResolver(state), fs: Filesystem{Policy: DefaultPolicy{}}, git: GitStatus{}}
}

func FeatureName() string {
	return workspaceFeatureName
}

func (h Handler) Handle(ctx context.Context, req protocol.WorkspaceRequest) protocol.WorkspaceResponse {
	resp := protocol.WorkspaceResponse{
		RequestID: req.RequestID,
		Operation: strings.TrimSpace(req.Operation),
		ProjectID: strings.TrimSpace(req.ProjectID),
		At:        time.Now().UTC(),
	}
	target, err := h.resolveTarget(ctx, resp.Operation, req)
	resp.Path = target.Rel
	if err != nil {
		resp.Status = "rejected"
		resp.Code = workspaceErrorCode(err, "invalid_workspace_path")
		resp.Message = err.Error()
		return resp
	}
	switch resp.Operation {
	case "list":
		result, err := h.fs.List(ctx, target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Code = workspaceErrorCode(err, "workspace_list_failed")
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.Entries = result.Entries
		resp.Warnings = result.Warnings
		resp.Partial = len(result.Warnings) > 0
	case "search":
		result, err := h.fs.Search(ctx, target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Code = workspaceErrorCode(err, "workspace_search_failed")
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.Matches = result.Matches
		resp.Warnings = result.Warnings
		resp.Partial = len(result.Warnings) > 0
	case "read":
		file, err := h.fs.Read(target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Code = workspaceErrorCode(err, "workspace_read_failed")
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.File = &file
	case "git_status":
		git, err := h.git.Status(ctx, target)
		if err != nil {
			resp.Status = "failed"
			resp.Code = workspaceErrorCode(err, "git_status_failed")
			resp.Message = err.Error()
			return resp
		}
		git = h.git.Limit(git, req.Limit)
		resp.Status = "ok"
		resp.Git = &git
	case "git_diff":
		diff, err := h.git.Diff(ctx, target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Code = workspaceErrorCode(err, "git_diff_failed")
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.Diff = &diff
	default:
		resp.Status = "rejected"
		resp.Code = "unsupported_operation"
		resp.Message = "unsupported workspace operation"
	}
	return resp
}

func (h Handler) resolveTarget(ctx context.Context, operation string, req protocol.WorkspaceRequest) (Target, error) {
	switch operation {
	case "git_status", "git_diff":
		return h.resolver.ResolveGit(ctx, strings.TrimSpace(req.ProjectID), req.Path)
	default:
		return h.resolver.Resolve(ctx, strings.TrimSpace(req.ProjectID), req.Path)
	}
}

func workspaceErrorCode(err error, fallback string) string {
	var workspaceErr WorkspaceError
	if err != nil && strings.TrimSpace(err.Error()) != "" && errors.As(err, &workspaceErr) && strings.TrimSpace(workspaceErr.Code) != "" {
		return workspaceErr.Code
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	return fallback
}
