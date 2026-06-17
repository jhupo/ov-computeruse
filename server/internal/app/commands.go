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

type dashCommandRequest struct {
	AgentID string           `json:"agent_id"`
	Command protocol.Command `json:"command"`
}

func (s *Server) handleDashCommand(w http.ResponseWriter, r *http.Request) {
	var req dashCommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid command payload")
		return
	}
	req.AgentID = strings.TrimSpace(req.AgentID)
	principal, identity, ok := s.authorizeAgentByID(w, r, req.AgentID)
	if !ok {
		return
	}
	req.Command.Kind = strings.TrimSpace(req.Command.Kind)
	if req.AgentID == "" || req.Command.Kind == "" {
		writeError(w, http.StatusBadRequest, "missing_command_fields", "agent_id and command.kind are required")
		return
	}
	if err := validateCommand(req.Command); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_command", err.Error())
		return
	}
	if err := s.validateCommandTargets(r.Context(), req.AgentID, req.Command); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_command_target", err.Error())
		return
	}
	if req.Command.CommandID == "" {
		req.Command.CommandID = protocol.NewID("cmd")
	}
	if req.Command.RunID == "" && commandCreatesRun(req.Command.Kind) {
		req.Command.RunID = protocol.NewID("run")
	}
	command, err := s.store.SaveCommand(r.Context(), req.AgentID, req.Command)
	if err != nil {
		s.log.ErrorContext(r.Context(), "save command failed", "agent_id", req.AgentID, "command_id", req.Command.CommandID, "error", err)
		writeError(w, http.StatusInternalServerError, "store_failed", "unable to save command")
		return
	}
	record, _ := s.dispatchStoredCommand(r, identity, command)
	s.log.InfoContext(r.Context(), "command accepted", "agent_id", req.AgentID, "user_id", principal.UserID, "command_id", command.CommandID, "kind", command.Kind, "status", record.Status)
	writeJSON(w, http.StatusAccepted, map[string]any{"command": record, "command_id": command.CommandID, "run_id": command.RunID})
}

func (s *Server) handleDashCommands(w http.ResponseWriter, r *http.Request) {
	_, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	commands, err := s.store.ListCommands(r.Context(), agentID, status, queryInt(r, "limit", 100))
	if err != nil {
		s.log.ErrorContext(r.Context(), "command list failed", "agent_id", agentID, "status", status, "error", err)
		writeError(w, http.StatusInternalServerError, "command_list_failed", "unable to load commands")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "status": status, "commands": commands})
}

func (s *Server) handleDashCommandDetail(w http.ResponseWriter, r *http.Request) {
	_, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	commandID := strings.TrimSpace(r.PathValue("command_id"))
	if commandID == "" {
		writeError(w, http.StatusBadRequest, "missing_command_id", "command_id is required")
		return
	}
	record, found, err := s.store.CommandByID(r.Context(), agentID, commandID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "command load failed", "agent_id", agentID, "command_id", commandID, "error", err)
		writeError(w, http.StatusInternalServerError, "command_load_failed", "unable to load command")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "command_not_found", "command not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "command": record})
}

func (s *Server) handleDashCommandRetry(w http.ResponseWriter, r *http.Request) {
	_, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	commandID := strings.TrimSpace(r.PathValue("command_id"))
	if commandID == "" {
		writeError(w, http.StatusBadRequest, "missing_command_id", "command_id is required")
		return
	}
	record, found, err := s.store.CommandByID(r.Context(), agentID, commandID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "command retry load failed", "agent_id", agentID, "command_id", commandID, "error", err)
		writeError(w, http.StatusInternalServerError, "command_load_failed", "unable to load command")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "command_not_found", "command not found")
		return
	}
	if !commandRetryable(record.Status) {
		writeError(w, http.StatusConflict, "command_not_retryable", "command is not in a retryable state")
		return
	}
	if err := s.validateCommandTargets(r.Context(), agentID, record.ToProtocol()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_command_target", err.Error())
		return
	}
	deadlineAt := time.Now().UTC().Add(10 * time.Minute)
	expiresAt := deadlineAt.Add(50 * time.Minute)
	if err := s.store.PrepareCommandRetry(r.Context(), agentID, commandID, deadlineAt, expiresAt); err != nil {
		s.log.ErrorContext(r.Context(), "command retry prepare failed", "agent_id", agentID, "command_id", commandID, "error", err)
		writeError(w, http.StatusInternalServerError, "command_retry_failed", "unable to prepare command retry")
		return
	}
	record, found, err = s.store.CommandByID(r.Context(), agentID, commandID)
	if err != nil || !found {
		s.log.ErrorContext(r.Context(), "command retry reload failed", "agent_id", agentID, "command_id", commandID, "error", err)
		writeError(w, http.StatusInternalServerError, "command_reload_failed", "unable to reload command")
		return
	}
	identity, err := s.store.AgentByID(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	refreshed, _ := s.dispatchStoredCommand(r, identity, record.ToProtocol())
	writeJSON(w, http.StatusAccepted, map[string]any{"agent_id": agentID, "command": refreshed})
}

func (s *Server) dispatchStoredCommand(r *http.Request, identity store.AgentIdentity, command protocol.Command) (store.CommandRecord, bool) {
	return s.dispatchCommand(r.Context(), identity, command)
}

func (s *Server) dispatchCommand(ctx context.Context, identity store.AgentIdentity, command protocol.Command) (store.CommandRecord, bool) {
	if !command.ExpiresAt.IsZero() && command.ExpiresAt.Before(time.Now().UTC()) {
		_ = s.store.MarkCommandExpired(ctx, identity.AgentID, command.CommandID, "command expired")
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	message := s.agentEnvelope(&AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret}, "command", command)
	if message == nil {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.CommandID, "unable to encode command")
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	if !s.hub.DispatchCommand(ctx, identity.AgentID, identity.UserID, command.CommandID, message) {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.CommandID, "agent offline or send queue full")
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
	return record, true
}

func validateCommand(command protocol.Command) error {
	kind := strings.TrimPrefix(command.Kind, "command.")
	switch kind {
	case "new_session":
		if !hasPromptPayload(command.Payload) {
			return errors.New("payload.prompt is required for new_session")
		}
	case "resume", "send":
		if strings.TrimSpace(command.SessionID) == "" {
			return errors.New("session_id is required for resume/send")
		}
		if !hasPromptPayload(command.Payload) {
			return errors.New("payload.prompt is required for resume/send")
		}
	case "stop":
		if strings.TrimSpace(command.RunID) == "" && strings.TrimSpace(command.SessionID) == "" {
			return errors.New("run_id or session_id is required for stop")
		}
	case "refresh_index":
	default:
		return errors.New("unsupported command kind")
	}
	return nil
}

func (s *Server) validateCommandTargets(ctx context.Context, agentID string, command protocol.Command) error {
	kind := strings.TrimPrefix(command.Kind, "command.")
	if strings.TrimSpace(command.ProjectID) != "" {
		exists, err := s.store.ProjectExists(ctx, agentID, strings.TrimSpace(command.ProjectID))
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("project_id does not belong to this agent")
		}
	}
	switch kind {
	case "resume", "send":
		exists, err := s.store.SessionExists(ctx, agentID, strings.TrimSpace(command.SessionID))
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("session_id does not belong to this agent")
		}
	case "stop":
		if strings.TrimSpace(command.RunID) != "" {
			exists, err := s.store.RunExists(ctx, agentID, strings.TrimSpace(command.RunID))
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("run_id does not belong to this agent")
			}
		}
		if strings.TrimSpace(command.SessionID) != "" {
			exists, err := s.store.SessionExists(ctx, agentID, strings.TrimSpace(command.SessionID))
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("session_id does not belong to this agent")
			}
		}
	}
	return nil
}

func hasPromptPayload(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var payload struct {
		Prompt string `json:"prompt"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return strings.TrimSpace(payload.Prompt) != "" || strings.TrimSpace(payload.Text) != ""
}

func commandRetryable(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "queued", "dispatch_failed", "dispatched", "expired", "failed":
		return true
	default:
		return false
	}
}

func commandCreatesRun(kind string) bool {
	switch strings.TrimPrefix(kind, "command.") {
	case "new_session", "resume", "send":
		return true
	default:
		return false
	}
}
