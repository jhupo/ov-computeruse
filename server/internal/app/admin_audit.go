package app

import (
	"net/http"
	"strings"
	"time"

	"ov-computeruse/server/internal/store"
)

func (s *Server) handleAdminAuditLogs(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	since, ok := queryTime(w, r, "since")
	if !ok {
		return
	}
	until, ok := queryTime(w, r, "until")
	if !ok {
		return
	}
	logs, err := s.store.ListAuditLogs(r.Context(), store.AuditLogFilter{
		UserID:  strings.TrimSpace(r.URL.Query().Get("user_id")),
		AgentID: strings.TrimSpace(r.URL.Query().Get("agent_id")),
		Action:  strings.TrimSpace(r.URL.Query().Get("action")),
		Since:   since,
		Until:   until,
		Limit:   queryInt(r, "limit", 200),
	})
	if err != nil {
		s.log.ErrorContext(r.Context(), "admin audit log list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "audit_log_list_failed", "unable to load audit logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit_logs": logs})
}

func queryTime(w http.ResponseWriter, r *http.Request, key string) (time.Time, bool) {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return time.Time{}, true
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, value)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_time", key+" must be RFC3339")
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
