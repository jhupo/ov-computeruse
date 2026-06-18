package workspace

import (
	"context"
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
}

func New(state State) Handler {
	return Handler{resolver: NewResolver(state), fs: Filesystem{Policy: DefaultPolicy{}}}
}

func FeatureName() string {
	return workspaceFeatureName
}

func (h Handler) Handle(ctx context.Context, req protocol.WorkspaceRequest) protocol.WorkspaceResponse {
	target, err := h.resolver.Resolve(ctx, strings.TrimSpace(req.ProjectID), req.Path)
	resp := protocol.WorkspaceResponse{
		RequestID: req.RequestID,
		Operation: strings.TrimSpace(req.Operation),
		ProjectID: strings.TrimSpace(req.ProjectID),
		Path:      target.Rel,
		At:        time.Now().UTC(),
	}
	if err != nil {
		resp.Status = "rejected"
		resp.Message = err.Error()
		return resp
	}
	switch resp.Operation {
	case "list":
		entries, err := h.fs.List(target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.Entries = entries
	case "read":
		file, err := h.fs.Read(target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.File = &file
	default:
		resp.Status = "rejected"
		resp.Message = "unsupported workspace operation"
	}
	return resp
}
