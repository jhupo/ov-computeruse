package app

import (
	"encoding/json"
	"net/http"
	"strings"

	"ov-computeruse/server/internal/protocol"
)

type dashCommandRequest struct {
	AgentID string           `json:"agent_id"`
	Command protocol.Command `json:"command"`
}

func (s *Server) handleDashCommand(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return
	}
	var req dashCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid command payload")
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	req.Command.Kind = strings.TrimSpace(req.Command.Kind)
	if req.AgentID == "" || req.Command.Kind == "" {
		writeError(w, http.StatusBadRequest, "missing_command_fields", "agent_id and command.kind are required")
		return
	}
	identity, err := s.store.AgentByID(r.Context(), req.AgentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	if !principal.Admin && identity.UserID != principal.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "agent does not belong to this user")
		return
	}
	if req.Command.CommandID == "" {
		req.Command.CommandID = protocol.NewID("cmd")
	}
	if err := s.store.SaveCommand(r.Context(), req.AgentID, req.Command); err != nil {
		s.log.ErrorContext(r.Context(), "save command failed", "agent_id", req.AgentID, "command_id", req.Command.CommandID, "error", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "unable to save command")
		return
	}
	message := s.agentEnvelope(&AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret}, "command", req.Command)
	if message == nil {
		writeError(w, http.StatusInternalServerError, "encode_failed", "unable to encode command")
		return
	}
	if !s.hub.DispatchCommand(r.Context(), req.AgentID, identity.UserID, req.Command.CommandID, message) {
		_ = s.store.MarkCommandFailed(r.Context(), req.AgentID, req.Command.CommandID)
		writeError(w, http.StatusConflict, "agent_offline", "agent is not connected")
		return
	}
	s.log.InfoContext(r.Context(), "command dispatched", "agent_id", req.AgentID, "user_id", identity.UserID, "command_id", req.Command.CommandID, "kind", req.Command.Kind)
	writeJSON(w, http.StatusAccepted, map[string]string{"command_id": req.Command.CommandID})
}
