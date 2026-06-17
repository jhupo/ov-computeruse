package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type dashLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type dashLoginResponse struct {
	Token     string        `json:"token"`
	ExpiresAt time.Time     `json:"expires_at"`
	Principal DashPrincipal `json:"principal"`
}

func (s *Server) handleDashLogin(w http.ResponseWriter, r *http.Request) {
	var req dashLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid login payload")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "missing_credentials", "username and password are required")
		return
	}
	principal, token, expiresAt, err := s.sessions.Login(r.Context(), req.Username, req.Password)
	if err != nil {
		s.log.WarnContext(r.Context(), "dash login rejected", "username", req.Username, "error", err)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}
	s.log.InfoContext(r.Context(), "dash login", "user_id", principal.UserID, "username", principal.Username)
	writeJSON(w, http.StatusOK, dashLoginResponse{Token: token, ExpiresAt: expiresAt, Principal: principal})
}
