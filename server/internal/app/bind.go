package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ov-computeruse/server/internal/security"
	"ov-computeruse/server/internal/store"
)

const bindRequestWindow = 5 * time.Minute

type bindRequest struct {
	Payload security.EncryptedPayload `json:"payload"`
}

type bindPlaintext struct {
	Username    string              `json:"username"`
	Password    string              `json:"password"`
	Device      store.DeviceProfile `json:"device"`
	Credential  store.Credential    `json:"credential"`
	RequestedAt time.Time           `json:"requested_at"`
	Nonce       string              `json:"nonce"`
}

type bindResponse struct {
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	DeviceID    string `json:"device_id"`
	AgentSecret string `json:"agent_secret"`
	ServerURL   string `json:"server_url"`
}

func (s *Server) handleBind(w http.ResponseWriter, r *http.Request) {
	var req bindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid bind payload")
		return
	}
	data, err := security.DecryptFromAgent(s.cfg.Token, req.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encrypted_payload", "invalid encrypted payload")
		return
	}
	var plain bindPlaintext
	if err := json.Unmarshal(data, &plain); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plaintext", "invalid bind plaintext")
		return
	}
	if err := s.validateBindFreshness(r, plain); err != nil {
		s.log.WarnContext(r.Context(), "agent bind freshness rejected", "username", plain.Username, "error", err)
		writeError(w, http.StatusBadRequest, "stale_bind_request", err.Error())
		return
	}
	identity, err := s.bind.Bind(r.Context(), plain.Username, plain.Password, plain.Device, plain.Credential)
	if err != nil {
		s.log.WarnContext(r.Context(), "agent bind rejected", "username", plain.Username, "error", err)
		writeError(w, http.StatusForbidden, "bind_rejected", "agent bind rejected")
		return
	}
	s.hub.DisconnectAgentBeforeEpoch(r.Context(), identity.AgentID, identity.AgentEpoch-1)
	s.log.InfoContext(r.Context(), "agent bound", "agent_id", identity.AgentID, "device_id", identity.DeviceID, "workspace_id", identity.WorkspaceID, "epoch", identity.AgentEpoch)
	writeJSON(w, http.StatusOK, bindResponse{
		AgentID:     identity.AgentID,
		WorkspaceID: identity.WorkspaceID,
		DeviceID:    identity.DeviceID,
		AgentSecret: identity.AgentSecret,
		ServerURL:   identity.ServerURL,
	})
}

func (s *Server) validateBindFreshness(r *http.Request, plain bindPlaintext) error {
	nonce := strings.TrimSpace(plain.Nonce)
	if nonce == "" {
		return errBindFreshness("bind nonce is required")
	}
	if len(nonce) < 16 || len(nonce) > 128 {
		return errBindFreshness("bind nonce size is invalid")
	}
	if !bindNonceSafe(nonce) {
		return errBindFreshness("bind nonce format is invalid")
	}
	if plain.RequestedAt.IsZero() {
		return errBindFreshness("requested_at is required")
	}
	now := time.Now().UTC()
	requestedAt := plain.RequestedAt.UTC()
	if requestedAt.Before(now.Add(-bindRequestWindow)) || requestedAt.After(now.Add(bindRequestWindow)) {
		return errBindFreshness("bind request is outside allowed time window")
	}
	if s.redis == nil {
		return errBindFreshness("bind replay store is unavailable")
	}
	key := "bind:nonce:" + nonce
	ok, err := s.redis.SetNX(r.Context(), key, "1", bindRequestWindow*2).Result()
	if err != nil {
		return errBindFreshness("bind replay store failed")
	}
	if !ok {
		return errBindFreshness("bind nonce was already used")
	}
	return nil
}

type errBindFreshness string

func (e errBindFreshness) Error() string {
	return string(e)
}

func bindNonceSafe(nonce string) bool {
	for _, r := range nonce {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
