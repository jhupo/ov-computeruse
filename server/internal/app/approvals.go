package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ov-computeruse/server/internal/protocol"
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
	decision := protocol.ApprovalDecision{
		ApprovalID: approvalID,
		Decision:   req.Decision,
		Reason:     strings.TrimSpace(req.Reason),
		DecidedBy:  principal.UserID,
		DecidedAt:  time.Now().UTC(),
	}
	if err := s.store.DecideApproval(r.Context(), approvalID, decision); err != nil {
		s.log.ErrorContext(r.Context(), "approval decision failed", "approval_id", approvalID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "approval_decision_failed", "unable to save approval decision")
		return
	}
	message := s.agentEnvelope(&AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret}, "approval.decision", decision)
	if message == nil {
		writeError(w, http.StatusInternalServerError, "encode_failed", "unable to encode approval decision")
		return
	}
	if !s.hub.DispatchCommand(r.Context(), identity.AgentID, identity.UserID, "", message) {
		writeError(w, http.StatusConflict, "agent_offline", "agent is not connected")
		return
	}
	s.hub.BroadcastDash(identity.UserID, protocol.Raw(map[string]any{"type": "approval.decided", "approval_id": approvalID, "decision": req.Decision, "agent_id": identity.AgentID}))
	writeJSON(w, http.StatusAccepted, map[string]any{"approval_id": approvalID, "decision": req.Decision})
}
