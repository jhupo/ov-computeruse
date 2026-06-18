package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

const workspaceRequestTimeout = 15 * time.Second

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
		writeError(w, http.StatusBadGateway, "workspace_request_failed", firstWorkspaceMessage(resp.Message, "workspace request failed"))
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
		writeError(w, http.StatusBadGateway, "workspace_request_failed", firstWorkspaceMessage(resp.Message, "workspace request failed"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": identity.AgentID, "project_id": req.ProjectID, "path": req.Path, "file": resp.File})
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
	if !s.hub.AgentMayBeOnline(ctx, identity.AgentID) {
		return protocol.WorkspaceResponse{}, http.StatusConflict, errors.New("agent is offline")
	}
	waitCh := s.registerWorkspaceWaiter(req.RequestID)
	defer s.unregisterWorkspaceWaiter(req.RequestID)
	message := s.agentEnvelope(&AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret, Epoch: identity.AgentEpoch}, "workspace.request", req)
	if message == nil {
		return protocol.WorkspaceResponse{}, http.StatusInternalServerError, errors.New("unable to encode workspace request")
	}
	if status := s.hub.DispatchEnvelope(ctx, identity.AgentID, identity.UserID, message); status != CommandDispatchDelivered {
		return protocol.WorkspaceResponse{}, http.StatusConflict, errors.New("agent is not available")
	}
	timer := time.NewTimer(workspaceRequestTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return protocol.WorkspaceResponse{}, http.StatusRequestTimeout, ctx.Err()
	case <-timer.C:
		return protocol.WorkspaceResponse{}, http.StatusGatewayTimeout, errors.New("workspace request timed out")
	case resp := <-waitCh:
		if resp.RequestID != req.RequestID {
			return protocol.WorkspaceResponse{}, http.StatusBadGateway, errors.New("workspace response request_id mismatch")
		}
		return resp, http.StatusOK, nil
	}
}

func (s *Server) registerWorkspaceWaiter(requestID string) chan protocol.WorkspaceResponse {
	ch := make(chan protocol.WorkspaceResponse, 1)
	s.workspaceMu.Lock()
	s.workspacePending[requestID] = ch
	s.workspaceMu.Unlock()
	return ch
}

func (s *Server) unregisterWorkspaceWaiter(requestID string) {
	s.workspaceMu.Lock()
	delete(s.workspacePending, requestID)
	s.workspaceMu.Unlock()
}

func (s *Server) resolveWorkspaceResponse(resp protocol.WorkspaceResponse) {
	if strings.TrimSpace(resp.RequestID) == "" {
		return
	}
	if s.resolveWorkspaceResponseLocal(resp) {
		return
	}
	s.publishWorkspaceResponse(resp)
}

func (s *Server) resolveWorkspaceResponseLocal(resp protocol.WorkspaceResponse) bool {
	s.workspaceMu.Lock()
	ch := s.workspacePending[resp.RequestID]
	s.workspaceMu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}

func (s *Server) publishWorkspaceResponse(resp protocol.WorkspaceResponse) {
	if s.redis == nil {
		return
	}
	raw, err := json.Marshal(WorkspaceResponseEnvelope{Origin: s.hub.InstanceID(), Response: resp})
	if err != nil {
		return
	}
	_ = s.redis.Publish(context.Background(), "ov:workspace:responses", raw).Err()
}

func (s *Server) subscribeWorkspaceResponses(ctx context.Context) {
	if s.redis == nil {
		return
	}
	pubsub := s.redis.Subscribe(ctx, "ov:workspace:responses")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var env WorkspaceResponseEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
			s.log.WarnContext(ctx, "invalid workspace response envelope", "error", err)
			continue
		}
		if env.Origin == s.hub.InstanceID() {
			continue
		}
		s.resolveWorkspaceResponseLocal(env.Response)
	}
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
