package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

type approvalDecisionRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason,omitempty"`
}

func (s *Server) handleDashApprovals(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	approvals, err := s.store.ListApprovals(r.Context(), principal.UserID, principal.Admin, status, queryInt(r, "limit", 100))
	if err != nil {
		s.log.ErrorContext(r.Context(), "approval list failed", "user_id", principal.UserID, "admin", principal.Admin, "error", err)
		writeError(w, http.StatusInternalServerError, "approval_list_failed", "unable to load approvals")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"approvals": approvals})
}

func (s *Server) handleDashApprovalDecision(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return
	}
	approvalID := strings.TrimSpace(r.PathValue("approval_id"))
	if approvalID == "" {
		writeError(w, http.StatusBadRequest, "missing_approval_id", "approval_id is required")
		return
	}
	var req approvalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid approval decision payload")
		return
	}
	req.Decision = strings.ToLower(strings.TrimSpace(req.Decision))
	if req.Decision != "approved" && req.Decision != "rejected" {
		writeError(w, http.StatusBadRequest, "invalid_decision", "decision must be approved or rejected")
		return
	}
	identity, err := s.store.ApprovalAgent(r.Context(), approvalID)
	if err != nil {
		writeError(w, http.StatusNotFound, "approval_not_found", "approval not found")
		return
	}
	if !principal.Admin && identity.UserID != principal.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "approval does not belong to this user")
		return
	}
	approval, found, err := s.store.ApprovalByID(r.Context(), approvalID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "approval load failed", "approval_id", approvalID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "approval_load_failed", "unable to load approval")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "approval_not_found", "approval not found")
		return
	}
	if approval.Status != "pending" {
		writeError(w, http.StatusConflict, "approval_already_decided", "approval is not pending")
		return
	}
	if approval.DecisionCommandID != "" {
		writeError(w, http.StatusConflict, "approval_decision_queued", "approval decision is already queued")
		return
	}
	decision := protocol.ApprovalDecision{
		ApprovalID: approvalID,
		Decision:   req.Decision,
		Reason:     strings.TrimSpace(req.Reason),
		DecidedBy:  principal.UserID,
		DecidedAt:  time.Now().UTC(),
	}
	now := time.Now().UTC()
	command := protocol.Command{
		CommandID:      protocol.NewID("cmd"),
		RunID:          approval.RunID,
		SessionID:      approval.SessionID,
		ProjectID:      approval.ProjectID,
		Kind:           "command.approval_decision",
		Payload:        protocol.Raw(decision),
		IdempotencyKey: "approval:" + approvalID + ":" + req.Decision,
		DeadlineAt:     now.Add(10 * time.Minute),
		ExpiresAt:      now.Add(1 * time.Hour),
	}
	if err := validateCommand(command); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_approval_command", err.Error())
		return
	}
	if err := s.validateCommandTargets(r.Context(), identity.AgentID, command); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_command_target", err.Error())
		return
	}
	if err := validateCommandCapabilities(identity, command); err != nil {
		writeError(w, http.StatusConflict, "command_not_supported", err.Error())
		return
	}
	if err := identity.AccessError(); err != nil {
		writeError(w, http.StatusConflict, "agent_disabled", err.Error())
		return
	}
	saved, err := s.store.SaveCommand(r.Context(), identity.AgentID, command)
	if err != nil {
		s.log.ErrorContext(r.Context(), "approval command save failed", "approval_id", approvalID, "agent_id", identity.AgentID, "error", err)
		writeError(w, http.StatusInternalServerError, "approval_command_failed", "unable to queue approval decision")
		return
	}
	if err := s.store.QueueApprovalDecision(r.Context(), approvalID, decision, saved.CommandID); err != nil {
		if errors.Is(err, store.ErrApprovalDecisionAlreadyQueued) {
			writeError(w, http.StatusConflict, "approval_decision_queued", "approval decision is already queued")
			return
		}
		s.log.ErrorContext(r.Context(), "approval decision queue state failed", "approval_id", approvalID, "command_id", saved.CommandID, "error", err)
		writeError(w, http.StatusInternalServerError, "approval_queue_failed", "unable to record approval decision")
		return
	}
	record, dispatched := s.dispatchStoredCommand(r, identity, saved)
	status := "queued"
	if dispatched {
		status = "dispatched"
	}
	s.hub.BroadcastDash(identity.UserID, protocol.Raw(map[string]any{"type": "approval.decision.queued", "approval_id": approvalID, "decision": req.Decision, "agent_id": identity.AgentID, "command_id": saved.CommandID, "status": status}))
	writeJSON(w, http.StatusAccepted, map[string]any{"approval_id": approvalID, "decision": req.Decision, "command": record})
}
