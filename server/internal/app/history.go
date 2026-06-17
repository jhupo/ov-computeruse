package app

import "net/http"

func (s *Server) handleDashHistoryMessages(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return
	}
	agentID := r.URL.Query().Get("agent_id")
	sessionID := r.URL.Query().Get("session_id")
	if agentID == "" || sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing_history_fields", "agent_id and session_id are required")
		return
	}
	identity, err := s.store.AgentByID(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return
	}
	if !principal.Admin && identity.UserID != principal.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "agent does not belong to this user")
		return
	}
	messages, err := s.store.HistoryMessages(r.Context(), agentID, sessionID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "history messages query failed", "agent_id", agentID, "session_id", sessionID, "error", err)
		writeError(w, http.StatusInternalServerError, "history_query_failed", "unable to load history messages")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "session_id": sessionID, "messages": messages})
}
