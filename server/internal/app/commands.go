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
	if err := identity.AccessError(); err != nil {
		writeError(w, http.StatusConflict, "agent_disabled", err.Error())
		return
	}
	if err := validateCommand(req.Command); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_command", err.Error())
		return
	}
	if err := validateCommandCapabilities(identity, req.Command); err != nil {
		writeError(w, http.StatusConflict, "command_not_supported", err.Error())
		return
	}
	if err := s.validateExecutionCredential(r.Context(), identity, req.Command); err != nil {
		writeError(w, http.StatusConflict, "credential_not_authorized", err.Error())
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
		if errors.Is(err, store.ErrSessionActive) {
			writeError(w, http.StatusConflict, "session_busy", err.Error())
			return
		}
		if errors.Is(err, store.ErrCommandIdempotencyConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict", err.Error())
			return
		}
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

func (s *Server) handleDashCommandAttempts(w http.ResponseWriter, r *http.Request) {
	_, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	commandID := strings.TrimSpace(r.PathValue("command_id"))
	if commandID == "" {
		writeError(w, http.StatusBadRequest, "missing_command_id", "command_id is required")
		return
	}
	if _, found, err := s.store.CommandByID(r.Context(), agentID, commandID); err != nil {
		s.log.ErrorContext(r.Context(), "command attempt command load failed", "agent_id", agentID, "command_id", commandID, "error", err)
		writeError(w, http.StatusInternalServerError, "command_load_failed", "unable to load command")
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "command_not_found", "command not found")
		return
	}
	attempts, err := s.store.ListCommandAttempts(r.Context(), agentID, commandID, queryInt(r, "limit", 200))
	if err != nil {
		s.log.ErrorContext(r.Context(), "command attempts query failed", "agent_id", agentID, "command_id", commandID, "error", err)
		writeError(w, http.StatusInternalServerError, "command_attempts_failed", "unable to load command attempts")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "command_id": commandID, "attempts": attempts})
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
	if strings.TrimPrefix(record.Kind, "command.") == "approval_decision" {
		writeError(w, http.StatusConflict, "approval_command_retry_not_allowed", "approval decision commands must be retried from the approval decision endpoint")
		return
	}
	if err := s.validateCommandTargets(r.Context(), agentID, record.ToProtocol()); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_command_target", err.Error())
		return
	}
	identity, err := s.store.AgentByID(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	if err := validateCommandCapabilities(identity, record.ToProtocol()); err != nil {
		writeError(w, http.StatusConflict, "command_not_supported", err.Error())
		return
	}
	if err := identity.AccessError(); err != nil {
		writeError(w, http.StatusConflict, "agent_disabled", err.Error())
		return
	}
	if err := s.validateExecutionCredential(r.Context(), identity, record.ToProtocol()); err != nil {
		writeError(w, http.StatusConflict, "credential_not_authorized", err.Error())
		return
	}
	deadlineAt := time.Now().UTC().Add(10 * time.Minute)
	expiresAt := deadlineAt.Add(50 * time.Minute)
	if err := s.store.PrepareCommandRetry(r.Context(), agentID, commandID, deadlineAt, expiresAt); err != nil {
		if errors.Is(err, store.ErrSessionActive) {
			writeError(w, http.StatusConflict, "session_busy", err.Error())
			return
		}
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
	refreshed, _ := s.dispatchStoredCommand(r, identity, record.ToProtocol())
	writeJSON(w, http.StatusAccepted, map[string]any{"agent_id": agentID, "command": refreshed})
}

func (s *Server) dispatchStoredCommand(r *http.Request, identity store.AgentIdentity, command protocol.Command) (store.CommandRecord, bool) {
	return s.claimAndDispatchCommand(r.Context(), identity, command)
}

func (s *Server) claimAndDispatchCommand(ctx context.Context, identity store.AgentIdentity, command protocol.Command) (store.CommandRecord, bool) {
	claimed, ok, err := s.store.ClaimCommand(ctx, identity.AgentID, command.CommandID, s.hub.InstanceID())
	if err != nil {
		s.log.WarnContext(ctx, "command claim failed", "agent_id", identity.AgentID, "command_id", command.CommandID, "error", err)
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	if !ok {
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	return s.dispatchCommand(ctx, identity, claimed.ToProtocol())
}

func (s *Server) dispatchCommand(ctx context.Context, identity store.AgentIdentity, command protocol.Command) (store.CommandRecord, bool) {
	if err := identity.AccessError(); err != nil {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.CommandID, err.Error())
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	if !command.ExpiresAt.IsZero() && command.ExpiresAt.Before(time.Now().UTC()) {
		_ = s.store.MarkCommandExpired(ctx, identity.AgentID, command.CommandID, "command expired")
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	if err := validateCommandCapabilities(identity, command); err != nil {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.CommandID, err.Error())
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	if err := s.validateExecutionCredential(ctx, identity, command); err != nil {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.CommandID, err.Error())
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	message := s.agentEnvelope(&AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret, Epoch: identity.AgentEpoch}, "command", command)
	if message == nil {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.CommandID, "unable to encode command")
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
	switch status := s.hub.DispatchCommand(ctx, identity.AgentID, identity.UserID, command.CommandID, message); status {
	case CommandDispatchDelivered:
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, true
	case CommandDispatchDelegated:
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	case CommandDispatchQueueFull:
		_ = s.store.MarkCommandDispatchFailed(ctx, identity.AgentID, command.CommandID, "agent send queue full")
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	default:
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.CommandID)
		return record, false
	}
}

func (s *Server) validateExecutionCredential(ctx context.Context, identity store.AgentIdentity, command protocol.Command) error {
	switch strings.TrimPrefix(command.Kind, "command.") {
	case "new_session", "resume", "send":
		return s.store.AgentCredentialValid(ctx, identity)
	default:
		return nil
	}
}

func validateCommand(command protocol.Command) error {
	kind := strings.TrimPrefix(command.Kind, "command.")
	switch kind {
	case "new_session":
		if strings.TrimSpace(command.ProjectID) == "" {
			return errors.New("project_id is required for new_session")
		}
		if strings.TrimSpace(command.SessionID) != "" {
			return errors.New("session_id must be empty for new_session")
		}
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
	case "approval_decision":
		if strings.TrimSpace(command.RunID) == "" {
			return errors.New("run_id is required for approval_decision")
		}
		var decision protocol.ApprovalDecision
		if len(command.Payload) == 0 || json.Unmarshal(command.Payload, &decision) != nil || strings.TrimSpace(decision.ApprovalID) == "" {
			return errors.New("payload.approval_id is required for approval_decision")
		}
	case "refresh_index":
	default:
		return errors.New("unsupported command kind")
	}
	return nil
}

func validateCommandCapabilities(identity store.AgentIdentity, command protocol.Command) error {
	var caps protocol.Capabilities
	if len(identity.Capabilities) == 0 {
		return errors.New("agent has not registered capabilities")
	}
	if err := json.Unmarshal(identity.Capabilities, &caps); err != nil {
		return errors.New("agent capabilities are invalid")
	}
	kind := strings.TrimPrefix(command.Kind, "command.")
	feature := "command." + kind
	if !capabilityHasFeature(caps, feature) {
		return errors.New("agent does not support " + feature)
	}
	switch kind {
	case "new_session", "resume", "send":
		if !caps.SupportsRuntime {
			return errors.New("agent does not support runtime execution")
		}
	case "approval_decision":
		if !capabilityHasFeature(caps, "approval.decision") {
			return errors.New("agent does not support approval decisions")
		}
	case "refresh_index":
		if !capabilityHasFeature(caps, "codex.scan") {
			return errors.New("agent does not support codex scanning")
		}
	}
	return nil
}

func capabilityHasFeature(caps protocol.Capabilities, feature string) bool {
	for _, item := range caps.Features {
		if strings.EqualFold(strings.TrimSpace(item), feature) {
			return true
		}
	}
	return false
}

func (s *Server) validateCommandTargets(ctx context.Context, agentID string, command protocol.Command) error {
	kind := strings.TrimPrefix(command.Kind, "command.")
	projectID := strings.TrimSpace(command.ProjectID)
	sessionID := strings.TrimSpace(command.SessionID)
	runID := strings.TrimSpace(command.RunID)
	if projectID != "" {
		if err := s.ensureCommandProject(ctx, agentID, projectID); err != nil {
			return err
		}
	}
	switch kind {
	case "new_session":
		return nil
	case "resume", "send":
		session, err := s.commandSessionTarget(ctx, agentID, sessionID)
		if err != nil {
			return err
		}
		return ensureCommandProjectMatchesSession(projectID, session)
	case "stop":
		var run store.RunTarget
		if runID != "" {
			target, err := s.commandRunTarget(ctx, agentID, runID)
			if err != nil {
				return err
			}
			run = target
		}
		var session store.SessionTarget
		if sessionID != "" {
			target, err := s.commandSessionTarget(ctx, agentID, sessionID)
			if err != nil {
				return err
			}
			session = target
		}
		return ensureStopTargetsMatch(projectID, sessionID, run, session)
	case "approval_decision":
		run, err := s.commandRunTarget(ctx, agentID, runID)
		if err != nil {
			return err
		}
		var session store.SessionTarget
		if sessionID != "" {
			target, err := s.commandSessionTarget(ctx, agentID, sessionID)
			if err != nil {
				return err
			}
			session = target
		}
		return ensureApprovalTargetsMatch(projectID, sessionID, run, session)
	}
	return nil
}

func (s *Server) ensureCommandProject(ctx context.Context, agentID, projectID string) error {
	exists, err := s.store.ProjectExists(ctx, agentID, projectID)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("project_id does not belong to this agent")
	}
	return nil
}

func (s *Server) commandSessionTarget(ctx context.Context, agentID, sessionID string) (store.SessionTarget, error) {
	target, found, err := s.store.SessionTarget(ctx, agentID, sessionID)
	if err != nil {
		return store.SessionTarget{}, err
	}
	if !found {
		return store.SessionTarget{}, errors.New("session_id does not belong to this agent")
	}
	return target, nil
}

func (s *Server) commandRunTarget(ctx context.Context, agentID, runID string) (store.RunTarget, error) {
	target, found, err := s.store.RunTarget(ctx, agentID, runID)
	if err != nil {
		return store.RunTarget{}, err
	}
	if !found {
		return store.RunTarget{}, errors.New("run_id does not belong to this agent")
	}
	return target, nil
}

func ensureCommandProjectMatchesSession(projectID string, session store.SessionTarget) error {
	if projectID != "" && session.ProjectID != "" && projectID != session.ProjectID {
		return errors.New("project_id does not match session_id")
	}
	return nil
}

func ensureStopTargetsMatch(projectID, sessionID string, run store.RunTarget, session store.SessionTarget) error {
	if run.ID != "" {
		if sessionID != "" && run.SessionID != "" && run.SessionID != sessionID {
			return errors.New("run_id does not match session_id")
		}
		if projectID != "" && run.ProjectID != "" && run.ProjectID != projectID {
			return errors.New("run_id does not match project_id")
		}
	}
	if session.ID != "" {
		if projectID != "" && session.ProjectID != "" && session.ProjectID != projectID {
			return errors.New("session_id does not match project_id")
		}
		if run.ID != "" && run.ProjectID != "" && session.ProjectID != "" && run.ProjectID != session.ProjectID {
			return errors.New("run_id project does not match session_id")
		}
	}
	return nil
}

func ensureApprovalTargetsMatch(projectID, sessionID string, run store.RunTarget, session store.SessionTarget) error {
	if sessionID != "" && run.SessionID != "" && run.SessionID != sessionID {
		return errors.New("run_id does not match session_id")
	}
	if projectID != "" && run.ProjectID != "" && run.ProjectID != projectID {
		return errors.New("run_id does not match project_id")
	}
	if session.ID != "" && projectID != "" && session.ProjectID != "" && session.ProjectID != projectID {
		return errors.New("session_id does not match project_id")
	}
	if session.ID != "" && run.ProjectID != "" && session.ProjectID != "" && run.ProjectID != session.ProjectID {
		return errors.New("run_id project does not match session_id")
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
