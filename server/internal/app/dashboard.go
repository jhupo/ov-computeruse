package app

import (
	"net/http"
	"strconv"
	"strings"

	"ov-computeruse/server/internal/store"
)

func (s *Server) handleDashMe(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"principal": principal})
}

func (s *Server) handleDashAgents(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return
	}
	agents, err := s.store.ListAgents(r.Context(), principal.UserID, principal.Admin)
	if err != nil {
		s.log.ErrorContext(r.Context(), "agent list failed", "user_id", principal.UserID, "admin", principal.Admin, "error", err)
		writeError(w, http.StatusInternalServerError, "agent_list_failed", "unable to load agents")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": agents})
}

func (s *Server) handleDashProjects(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	projects, err := s.store.ListProjects(r.Context(), agentID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "project list failed", "agent_id", agentID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "project_list_failed", "unable to load projects")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "projects": projects})
}

func (s *Server) handleDashSessions(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	sessions, err := s.store.ListSessions(r.Context(), agentID, projectID, queryInt(r, "limit", 200))
	if err != nil {
		s.log.ErrorContext(r.Context(), "session list failed", "agent_id", agentID, "project_id", projectID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "session_list_failed", "unable to load sessions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "project_id": projectID, "sessions": sessions})
}

func (s *Server) handleDashRuns(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	runs, err := s.store.ListRuns(r.Context(), agentID, sessionID, queryInt(r, "limit", 100))
	if err != nil {
		s.log.ErrorContext(r.Context(), "run list failed", "agent_id", agentID, "session_id", sessionID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "run_list_failed", "unable to load runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "session_id": sessionID, "runs": runs})
}

func (s *Server) handleDashRunEvents(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing_run_id", "run_id is required")
		return
	}
	events, err := s.store.ListRunEvents(r.Context(), agentID, runID, uint64(queryInt(r, "after_seq", 0)), queryInt(r, "limit", 300))
	if err != nil {
		s.log.ErrorContext(r.Context(), "run event list failed", "agent_id", agentID, "run_id", runID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "run_event_list_failed", "unable to load run events")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "run_id": runID, "events": events})
}

func (s *Server) handleDashRunTimeline(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "missing_run_id", "run_id is required")
		return
	}
	steps, err := s.store.ListRunSteps(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "run step list failed", "agent_id", agentID, "run_id", runID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "run_step_list_failed", "unable to load run timeline")
		return
	}
	messages, err := s.store.ListRunMessages(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "run message list failed", "agent_id", agentID, "run_id", runID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "run_message_list_failed", "unable to load run messages")
		return
	}
	tools, err := s.store.ListToolCalls(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "tool call list failed", "agent_id", agentID, "run_id", runID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "tool_call_list_failed", "unable to load tool calls")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "run_id": runID, "timeline": steps, "messages": messages, "tool_calls": tools})
}

func (s *Server) handleDashRuntimeSessions(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	runtimeSessions, err := s.store.ListRuntimeSessions(r.Context(), agentID, sessionID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "runtime session list failed", "agent_id", agentID, "session_id", sessionID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "runtime_session_list_failed", "unable to load runtime sessions")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "session_id": sessionID, "runtime_sessions": runtimeSessions})
}

func (s *Server) handleDashHistoryItems(w http.ResponseWriter, r *http.Request) {
	principal, agentID, ok := s.authorizeAgentQuery(w, r)
	if !ok {
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, "missing_session_id", "session_id is required")
		return
	}
	items, err := s.store.ListHistoryItems(r.Context(), agentID, sessionID, queryInt(r, "after_index", -1), queryInt(r, "limit", 300))
	if err != nil {
		s.log.ErrorContext(r.Context(), "history item list failed", "agent_id", agentID, "session_id", sessionID, "user_id", principal.UserID, "error", err)
		writeError(w, http.StatusInternalServerError, "history_item_list_failed", "unable to load history items")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agent_id": agentID, "session_id": sessionID, "items": items})
}

func (s *Server) authorizeAgentQuery(w http.ResponseWriter, r *http.Request) (DashPrincipal, string, bool) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return DashPrincipal{}, "", false
	}
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "missing_agent_id", "agent_id is required")
		return DashPrincipal{}, "", false
	}
	if _, _, ok := s.authorizeAgentIdentity(w, r, principal, agentID); !ok {
		return DashPrincipal{}, "", false
	}
	return principal, agentID, true
}

func (s *Server) authorizeAgentByID(w http.ResponseWriter, r *http.Request, agentID string) (DashPrincipal, store.AgentIdentity, bool) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "dash session is required")
		return DashPrincipal{}, store.AgentIdentity{}, false
	}
	if agentID == "" {
		writeError(w, http.StatusBadRequest, "missing_agent_id", "agent_id is required")
		return DashPrincipal{}, store.AgentIdentity{}, false
	}
	return s.authorizeAgentIdentity(w, r, principal, agentID)
}

func (s *Server) authorizeAgentIdentity(w http.ResponseWriter, r *http.Request, principal DashPrincipal, agentID string) (DashPrincipal, store.AgentIdentity, bool) {
	identity, err := s.store.AgentByID(r.Context(), agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "agent_not_found", "agent not found")
		return DashPrincipal{}, store.AgentIdentity{}, false
	}
	if !principal.Admin && identity.UserID != principal.UserID {
		writeError(w, http.StatusForbidden, "forbidden", "agent does not belong to this user")
		return DashPrincipal{}, store.AgentIdentity{}, false
	}
	return principal, identity, true
}

func queryInt(r *http.Request, key string, fallback int) int {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
