package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

func (s *Server) handleDashWorkspaceTree(w http.ResponseWriter, r *http.Request) {
	_, identity, req, ok := s.workspaceRequestFromQuery(w, r, "list")
	if !ok {
		return
	}
	req.Depth = queryInt(r, "depth", 1)
	req.Limit = queryInt(r, "limit", 500)
	resp, status, err := s.sendWorkspaceRequest(r.Context(), identity, req)
	if err != nil {
		writeError(w, status, workspaceErrorCode(status), err.Error())
		return
	}
	if resp.Status != "ok" {
		writeError(w, http.StatusBadGateway, firstWorkspaceCode(resp.Code, "workspace_request_failed"), firstWorkspaceMessage(resp.Message, "workspace request failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": identity.AgentID, "project_id": req.ProjectID, "path": req.Path, "entries": resp.Entries})
}

func (s *Server) handleDashWorkspaceFile(w http.ResponseWriter, r *http.Request) {
	_, identity, req, ok := s.workspaceRequestFromQuery(w, r, "read")
	if !ok {
		return
	}
	req.MaxBytes = int64(queryInt(r, "max_bytes", 256<<10))
	resp, status, err := s.sendWorkspaceRequest(r.Context(), identity, req)
	if err != nil {
		writeError(w, status, workspaceErrorCode(status), err.Error())
		return
	}
	if resp.Status != "ok" {
		writeError(w, http.StatusBadGateway, firstWorkspaceCode(resp.Code, "workspace_request_failed"), firstWorkspaceMessage(resp.Message, "workspace request failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": identity.AgentID, "project_id": req.ProjectID, "path": req.Path, "file": resp.File})
}

func (s *Server) handleDashWorkspaceSearch(w http.ResponseWriter, r *http.Request) {
	_, identity, req, ok := s.workspaceRequestFromQuery(w, r, "search")
	if !ok {
		return
	}
	req.Query = strings.TrimSpace(r.URL.Query().Get("q"))
	if req.Query == "" {
		req.Query = strings.TrimSpace(r.URL.Query().Get("query"))
	}
	if req.Query == "" {
		writeError(w, http.StatusBadRequest, "missing_query", "q or query is required")
		return
	}
	req.Limit = queryInt(r, "limit", 100)
	req.Depth = queryInt(r, "depth", 8)
	resp, status, err := s.sendWorkspaceRequest(r.Context(), identity, req)
	if err != nil {
		writeError(w, status, workspaceErrorCode(status), err.Error())
		return
	}
	if resp.Status != "ok" {
		writeError(w, http.StatusBadGateway, firstWorkspaceCode(resp.Code, "workspace_request_failed"), firstWorkspaceMessage(resp.Message, "workspace request failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": identity.AgentID, "project_id": req.ProjectID, "query": req.Query, "matches": resp.Matches})
}

func (s *Server) handleDashWorkspaceGitStatus(w http.ResponseWriter, r *http.Request) {
	_, identity, req, ok := s.workspaceRequestFromQuery(w, r, "git_status")
	if !ok {
		return
	}
	req.Limit = queryInt(r, "limit", 500)
	resp, status, err := s.sendWorkspaceRequest(r.Context(), identity, req)
	if err != nil {
		writeError(w, status, workspaceErrorCode(status), err.Error())
		return
	}
	if resp.Status != "ok" {
		writeError(w, http.StatusBadGateway, firstWorkspaceCode(resp.Code, "workspace_request_failed"), firstWorkspaceMessage(resp.Message, "workspace request failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": identity.AgentID, "project_id": req.ProjectID, "git": resp.Git})
}

func (s *Server) handleDashWorkspaceGitDiff(w http.ResponseWriter, r *http.Request) {
	_, identity, req, ok := s.workspaceRequestFromQuery(w, r, "git_diff")
	if !ok {
		return
	}
	req.Staged = queryBool(r, "staged", false)
	req.MaxBytes = int64(queryInt(r, "max_bytes", 512<<10))
	resp, status, err := s.sendWorkspaceRequest(r.Context(), identity, req)
	if err != nil {
		writeError(w, status, workspaceErrorCode(status), err.Error())
		return
	}
	if resp.Status != "ok" {
		writeError(w, http.StatusBadGateway, firstWorkspaceCode(resp.Code, "workspace_request_failed"), firstWorkspaceMessage(resp.Message, "workspace request failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": identity.AgentID, "project_id": req.ProjectID, "path": req.Path, "diff": resp.Diff})
}

func (s *Server) workspaceRequestFromQuery(w http.ResponseWriter, r *http.Request, operation string) (DashPrincipal, store.AgentIdentity, protocol.WorkspaceRequest, bool) {
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	principal, identity, ok := s.authorizeAgentByID(w, r, agentID)
	if !ok {
		return DashPrincipal{}, store.AgentIdentity{}, protocol.WorkspaceRequest{}, false
	}
	if err := identity.AccessError(); err != nil {
		writeError(w, http.StatusConflict, "agent_disabled", err.Error())
		return DashPrincipal{}, store.AgentIdentity{}, protocol.WorkspaceRequest{}, false
	}
	if err := validateWorkspaceCapability(identity, operation); err != nil {
		writeError(w, http.StatusConflict, "workspace_not_supported", err.Error())
		return DashPrincipal{}, store.AgentIdentity{}, protocol.WorkspaceRequest{}, false
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		writeError(w, http.StatusBadRequest, "missing_project_id", "project_id is required")
		return DashPrincipal{}, store.AgentIdentity{}, protocol.WorkspaceRequest{}, false
	}
	exists, err := s.store.ProjectExists(r.Context(), identity.AgentID, projectID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "workspace project lookup failed", "agent_id", identity.AgentID, "project_id", projectID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "project_lookup_failed", "unable to load project")
		return DashPrincipal{}, store.AgentIdentity{}, protocol.WorkspaceRequest{}, false
	}
	if !exists {
		writeError(w, http.StatusNotFound, "project_not_found", "project not found")
		return DashPrincipal{}, store.AgentIdentity{}, protocol.WorkspaceRequest{}, false
	}
	return principal, identity, protocol.WorkspaceRequest{
		RequestID: protocol.NewID("wsreq"),
		Operation: operation,
		ProjectID: projectID,
		Path:      strings.TrimSpace(r.URL.Query().Get("path")),
	}, true
}

func (s *Server) sendWorkspaceRequest(ctx context.Context, identity store.AgentIdentity, req protocol.WorkspaceRequest) (protocol.WorkspaceResponse, int, error) {
	message := s.agentEnvelope(&AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret, Epoch: identity.AgentEpoch}, "workspace.request", req)
	if message == nil {
		return protocol.WorkspaceResponse{}, http.StatusInternalServerError, errors.New("unable to encode workspace request")
	}
	return s.workspace.Send(ctx, identity, req, message)
}

func workspaceErrorCode(status int) string {
	switch status {
	case http.StatusConflict:
		return "agent_offline"
	case http.StatusGatewayTimeout:
		return "workspace_timeout"
	default:
		return "workspace_request_failed"
	}
}

func firstWorkspaceMessage(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func firstWorkspaceCode(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func validateWorkspaceCapability(identity store.AgentIdentity, operation string) error {
	var caps protocol.Capabilities
	if len(identity.Capabilities) == 0 {
		return errors.New("agent has not registered capabilities")
	}
	if err := json.Unmarshal(identity.Capabilities, &caps); err != nil {
		return errors.New("agent capabilities are invalid")
	}
	switch operation {
	case "list", "read", "search":
		if !capabilityHasFeature(caps, "workspace.files") {
			return errors.New("agent does not support workspace files")
		}
	case "git_status":
		if !caps.SupportsGit || !capabilityHasFeature(caps, "git.status") {
			return errors.New("agent does not support git status")
		}
	case "git_diff":
		if !caps.SupportsGit || !capabilityHasFeature(caps, "git.diff") {
			return errors.New("agent does not support git diff")
		}
	default:
		return errors.New("unsupported workspace operation")
	}
	return nil
}
