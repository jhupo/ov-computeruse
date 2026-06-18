package app

import (
	"encoding/json"
	"net/http"
	"strings"

	"ov-computeruse/server/internal/store"
)

type adminUserRequest struct {
	ID       string `json:"id,omitempty"`
	Username string `json:"username"`
	Password string `json:"password,omitempty"`
}

type adminUserKeyRequest struct {
	ID             string `json:"id,omitempty"`
	Name           string `json:"name,omitempty"`
	BaseURL        string `json:"base_url"`
	KeyFingerprint string `json:"key_fingerprint"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
}

type adminAccessRequest struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	users, err := s.store.ListUsers(r.Context(), queryBool(r, "include_disabled", true))
	if err != nil {
		s.log.ErrorContext(r.Context(), "admin user list failed", "error", err)
		writeError(w, http.StatusInternalServerError, "user_list_failed", "unable to load users")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

func (s *Server) handleAdminUserUpsert(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	var req adminUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid user payload")
		return
	}
	user, err := s.store.UpsertUser(r.Context(), store.UserUpsert{
		ID:       strings.TrimSpace(req.ID),
		Username: strings.TrimSpace(req.Username),
		Password: req.Password,
		Actor:    principalActor(principal),
	})
	if err != nil {
		s.log.WarnContext(r.Context(), "admin user upsert rejected", "username", req.Username, "error", err)
		writeError(w, http.StatusBadRequest, "user_upsert_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) handleAdminUserDisable(w http.ResponseWriter, r *http.Request) {
	s.handleAdminUserAccess(w, r, false)
}

func (s *Server) handleAdminUserEnable(w http.ResponseWriter, r *http.Request) {
	s.handleAdminUserAccess(w, r, true)
}

func (s *Server) handleAdminUserAccess(w http.ResponseWriter, r *http.Request, enabled bool) {
	principal, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	if userID == "" {
		writeError(w, http.StatusBadRequest, "missing_user_id", "user_id is required")
		return
	}
	var req adminAccessRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid access payload")
			return
		}
	}
	user, err := s.store.SetUserAccess(r.Context(), userID, store.AccessChange{Enabled: enabled, Reason: strings.TrimSpace(req.Reason), Actor: principalActor(principal)})
	if err != nil {
		s.log.WarnContext(r.Context(), "admin user access change failed", "user_id", userID, "enabled", enabled, "error", err)
		writeError(w, http.StatusBadRequest, "user_access_failed", err.Error())
		return
	}
	if !enabled {
		s.hub.DisconnectUserAgents(r.Context(), userID)
	}
	action := "user.disabled"
	if enabled {
		action = "user.enabled"
	}
	_ = s.store.SaveAuditLog(r.Context(), user.ID, "", action, map[string]any{"actor": principalActor(principal), "reason": req.Reason})
	writeJSON(w, http.StatusOK, map[string]any{"user": user, "enabled": enabled})
}

func (s *Server) handleAdminUserKeys(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAdmin(w, r); !ok {
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	if userID == "" {
		writeError(w, http.StatusBadRequest, "missing_user_id", "user_id is required")
		return
	}
	keys, err := s.store.ListUserKeys(r.Context(), userID, queryBool(r, "include_disabled", true))
	if err != nil {
		s.log.ErrorContext(r.Context(), "admin user key list failed", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "user_key_list_failed", "unable to load user keys")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user_id": userID, "keys": keys})
}

func (s *Server) handleAdminUserKeyUpsert(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	if userID == "" {
		writeError(w, http.StatusBadRequest, "missing_user_id", "user_id is required")
		return
	}
	if _, found, err := s.store.UserByID(r.Context(), userID); err != nil {
		s.log.ErrorContext(r.Context(), "admin user lookup failed", "user_id", userID, "error", err)
		writeError(w, http.StatusInternalServerError, "user_lookup_failed", "unable to load user")
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "user_not_found", "user not found")
		return
	}
	var req adminUserKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid user key payload")
		return
	}
	key, err := s.store.UpsertUserKey(r.Context(), store.UserKeyUpsert{
		ID:             strings.TrimSpace(req.ID),
		UserID:         userID,
		Name:           strings.TrimSpace(req.Name),
		BaseURL:        strings.TrimSpace(req.BaseURL),
		KeyFingerprint: strings.TrimSpace(req.KeyFingerprint),
		Provider:       strings.TrimSpace(req.Provider),
		Model:          strings.TrimSpace(req.Model),
		Actor:          principalActor(principal),
	})
	if err != nil {
		s.log.WarnContext(r.Context(), "admin user key upsert rejected", "user_id", userID, "error", err)
		writeError(w, http.StatusBadRequest, "user_key_upsert_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key})
}

func (s *Server) handleAdminUserKeyDisable(w http.ResponseWriter, r *http.Request) {
	s.handleAdminUserKeyAccess(w, r, false)
}

func (s *Server) handleAdminUserKeyEnable(w http.ResponseWriter, r *http.Request) {
	s.handleAdminUserKeyAccess(w, r, true)
}

func (s *Server) handleAdminUserKeyAccess(w http.ResponseWriter, r *http.Request, enabled bool) {
	principal, ok := s.requireAdmin(w, r)
	if !ok {
		return
	}
	userID := strings.TrimSpace(r.PathValue("user_id"))
	keyID := strings.TrimSpace(r.PathValue("key_id"))
	if userID == "" || keyID == "" {
		writeError(w, http.StatusBadRequest, "missing_key_fields", "user_id and key_id are required")
		return
	}
	key, found, err := s.store.UserKeyByID(r.Context(), keyID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "admin user key lookup failed", "user_id", userID, "key_id", keyID, "error", err)
		writeError(w, http.StatusInternalServerError, "user_key_lookup_failed", "unable to load user key")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "user_key_not_found", "user key not found")
		return
	}
	if key.UserID != userID {
		writeError(w, http.StatusForbidden, "forbidden", "user key does not belong to this user")
		return
	}
	var req adminAccessRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid access payload")
			return
		}
	}
	key, err = s.store.SetUserKeyAccess(r.Context(), keyID, store.AccessChange{Enabled: enabled, Reason: strings.TrimSpace(req.Reason), Actor: principalActor(principal)})
	if err != nil {
		s.log.WarnContext(r.Context(), "admin user key access change failed", "user_id", userID, "key_id", keyID, "enabled", enabled, "error", err)
		writeError(w, http.StatusBadRequest, "user_key_access_failed", err.Error())
		return
	}
	if !enabled {
		s.hub.DisconnectUserAgents(r.Context(), userID)
	}
	action := "user_key.disabled"
	if enabled {
		action = "user_key.enabled"
	}
	_ = s.store.SaveAuditLog(r.Context(), userID, "", action, map[string]any{"actor": principalActor(principal), "key_id": keyID, "reason": req.Reason})
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "enabled": enabled})
}

func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) (DashPrincipal, bool) {
	principal, ok := s.requireDash(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "admin session is required")
		return DashPrincipal{}, false
	}
	if !principal.Admin {
		writeError(w, http.StatusForbidden, "forbidden", "admin access is required")
		return DashPrincipal{}, false
	}
	return principal, true
}

func principalActor(principal DashPrincipal) string {
	if principal.UserID != "" {
		return principal.UserID
	}
	if principal.Admin {
		return "admin"
	}
	return "unknown"
}
