package app

import (
	"encoding/json"
	"net/http"
	"time"

	"ov-computeruse/server/internal/security"
	"ov-computeruse/server/internal/store"
)

type bindRequest struct {
	ServerKeyID string                    `json:"server_key_id"`
	Payload     security.EncryptedPayload `json:"payload"`
}

type bindPlaintext struct {
	Username    string              `json:"username"`
	Password    string              `json:"password"`
	Device      store.DeviceProfile `json:"device"`
	Credential  store.Credential    `json:"credential"`
	RequestedAt time.Time           `json:"requested_at"`
}

type bindResponse struct {
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	DeviceID    string `json:"device_id"`
	AgentSecret string `json:"agent_secret"`
	ServerURL   string `json:"server_url"`
	ServerKeyID string `json:"server_key_id"`
}

func (s *Server) handleBind(w http.ResponseWriter, r *http.Request) {
	var req bindRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid bind payload")
		return
	}
	if req.ServerKeyID != s.cfg.ServerKeyID || req.Payload.ServerKeyID != s.cfg.ServerKeyID {
		writeError(w, http.StatusBadRequest, "server_key_mismatch", "server key id mismatch")
		return
	}
	data, err := security.DecryptFromAgent(s.cfg.ServerPrivateKeyPEM, req.Payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_encrypted_payload", "invalid encrypted payload")
		return
	}
	var plain bindPlaintext
	if err := json.Unmarshal(data, &plain); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_plaintext", "invalid bind plaintext")
		return
	}
	identity, err := s.bind.Bind(r.Context(), plain.Username, plain.Password, plain.Device, plain.Credential)
	if err != nil {
		s.log.WarnContext(r.Context(), "agent bind rejected", "username", plain.Username, "error", err)
		writeError(w, http.StatusForbidden, "bind_rejected", "agent bind rejected")
		return
	}
	s.log.InfoContext(r.Context(), "agent bound", "agent_id", identity.AgentID, "device_id", identity.DeviceID, "workspace_id", identity.WorkspaceID)
	writeJSON(w, http.StatusOK, bindResponse{
		AgentID:     identity.AgentID,
		WorkspaceID: identity.WorkspaceID,
		DeviceID:    identity.DeviceID,
		AgentSecret: identity.AgentSecret,
		ServerURL:   identity.ServerURL,
		ServerKeyID: identity.ServerKeyID,
	})
}
