package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

type agentAccessRequest struct {
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type agentAccessResponse struct {
	AgentID              string    `json:"agent_id"`
	DeviceID             string    `json:"device_id"`
	Scope                string    `json:"scope"`
	Enabled              bool      `json:"enabled"`
	Disabled             bool      `json:"disabled"`
	DisabledAt           time.Time `json:"disabled_at,omitempty"`
	DisabledReason       string    `json:"disabled_reason,omitempty"`
	AgentDisabledAt      time.Time `json:"agent_disabled_at,omitempty"`
	DeviceDisabledReason string    `json:"device_disabled_reason,omitempty"`
	DeviceDisabledAt     time.Time `json:"device_disabled_at,omitempty"`
}

func (s *Server) handleDashAgentDisable(w http.ResponseWriter, r *http.Request) {
	s.handleDashAgentAccess(w, r, false)
}

func (s *Server) handleDashAgentEnable(w http.ResponseWriter, r *http.Request) {
	s.handleDashAgentAccess(w, r, true)
}

func (s *Server) handleDashAgentAccess(w http.ResponseWriter, r *http.Request, enabled bool) {
	agentID := strings.TrimSpace(r.PathValue("agent_id"))
	principal, identity, ok := s.authorizeAgentByID(w, r, agentID)
	if !ok {
		return
	}
	var req agentAccessRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", "invalid agent access payload")
			return
		}
	}
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "agent"
	}
	if scope != "agent" && scope != "device" {
		writeError(w, http.StatusBadRequest, "invalid_scope", "scope must be agent or device")
		return
	}
	change := store.AccessChange{Enabled: enabled, Reason: strings.TrimSpace(req.Reason), Actor: principal.UserID}
	next := identity
	var err error
	switch scope {
	case "agent":
		next, err = s.store.SetAgentAccess(r.Context(), identity.AgentID, change)
	case "device":
		if err = s.store.SetDeviceAccess(r.Context(), identity.DeviceID, change); err == nil {
			next, err = s.store.AgentByID(r.Context(), identity.AgentID)
		}
	}
	if err != nil {
		s.log.ErrorContext(r.Context(), "agent access change failed", "agent_id", identity.AgentID, "device_id", identity.DeviceID, "scope", scope, "enabled", enabled, "error", err)
		writeError(w, http.StatusInternalServerError, "agent_access_failed", "unable to update agent access")
		return
	}
	action := "agent.disabled"
	if enabled {
		action = "agent.enabled"
	}
	_ = s.store.SaveAuditLog(r.Context(), identity.UserID, identity.AgentID, action, map[string]any{"scope": scope, "device_id": identity.DeviceID, "reason": change.Reason, "actor": principal.UserID})
	if !enabled {
		s.hub.DisconnectAgent(r.Context(), identity.AgentID)
	}
	s.hub.BroadcastDash(identity.UserID, protocol.Raw(map[string]any{
		"type":      "agent.access.changed",
		"agent_id":  identity.AgentID,
		"device_id": identity.DeviceID,
		"payload": map[string]any{
			"scope":    scope,
			"enabled":  enabled,
			"reason":   change.Reason,
			"disabled": next.AccessError() != nil,
		},
	}))
	writeJSON(w, http.StatusOK, map[string]any{"agent": agentAccessPayload(next, scope, enabled)})
}

func agentAccessPayload(identity store.AgentIdentity, scope string, enabled bool) agentAccessResponse {
	return agentAccessResponse{
		AgentID:              identity.AgentID,
		DeviceID:             identity.DeviceID,
		Scope:                scope,
		Enabled:              enabled,
		Disabled:             identity.AccessError() != nil,
		DisabledAt:           firstAccessDisabledAt(identity),
		DisabledReason:       identity.DisabledReason,
		AgentDisabledAt:      identity.DisabledAt,
		DeviceDisabledReason: identity.DeviceDisabledReason,
		DeviceDisabledAt:     identity.DeviceDisabledAt,
	}
}

func firstAccessDisabledAt(identity store.AgentIdentity) time.Time {
	if !identity.DisabledAt.IsZero() {
		return identity.DisabledAt
	}
	return identity.DeviceDisabledAt
}
