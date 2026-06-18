package app

import (
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"ov-computeruse/server/internal/protocol"
)

type dashWSMessage struct {
	Type     string `json:"type"`
	AgentID  string `json:"agent_id,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	AfterSeq uint64 `json:"after_seq,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

func (s *Server) handleDashWS(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireDash(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	dash := &DashConn{ID: protocol.NewID("dash"), Principal: principal, Conn: conn, Send: make(chan []byte, 128), Subscriptions: map[string]DashSubscription{}}
	s.hub.AddDash(dash)
	s.log.InfoContext(r.Context(), "dash connected", "dash_id", dash.ID, "user_id", principal.UserID, "admin", principal.Admin)
	go s.dashWriter(dash)
	s.dashReader(r, dash)
}

func (s *Server) dashWriter(dash *DashConn) {
	for data := range dash.Send {
		if err := dash.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

func (s *Server) dashReader(r *http.Request, dash *DashConn) {
	defer func() {
		s.hub.RemoveDash(dash.ID)
		_ = dash.Conn.Close()
		close(dash.Send)
		s.log.InfoContext(r.Context(), "dash disconnected", "dash_id", dash.ID, "user_id", dash.Principal.UserID, "admin", dash.Principal.Admin)
	}()
	for {
		var message dashWSMessage
		if err := dash.Conn.ReadJSON(&message); err != nil {
			return
		}
		s.handleDashWSMessage(r, dash, message)
	}
}

func (s *Server) handleDashWSMessage(r *http.Request, dash *DashConn, message dashWSMessage) {
	switch strings.TrimSpace(message.Type) {
	case "run.subscribe":
		s.handleDashRunSubscribe(r, dash, message)
	case "run.unsubscribe":
		agentID := strings.TrimSpace(message.AgentID)
		runID := strings.TrimSpace(message.RunID)
		dash.mu.Lock()
		delete(dash.Subscriptions, dashSubscriptionKey(agentID, runID))
		dash.mu.Unlock()
		s.sendDash(dash, "run.unsubscribed", map[string]any{"agent_id": agentID, "run_id": runID})
	case "ping":
		s.sendDash(dash, "pong", map[string]any{"at": time.Now().UTC()})
	default:
		s.sendDashError(dash, "unsupported_message", "unsupported dash websocket message type")
	}
}

func (s *Server) handleDashRunSubscribe(r *http.Request, dash *DashConn, message dashWSMessage) {
	agentID := strings.TrimSpace(message.AgentID)
	runID := strings.TrimSpace(message.RunID)
	if agentID == "" || runID == "" {
		s.sendDashError(dash, "missing_run_subscription", "agent_id and run_id are required")
		return
	}
	if _, _, ok := s.authorizeDashWSAgent(r, dash, agentID); !ok {
		s.sendDashError(dash, "forbidden", "agent does not belong to this user")
		return
	}
	exists, err := s.store.RunExists(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "dash run subscribe lookup failed", "dash_id", dash.ID, "agent_id", agentID, "run_id", runID, "error", err)
		s.sendDashError(dash, "run_snapshot_failed", "unable to load run")
		return
	}
	if !exists {
		s.sendDashError(dash, "run_not_found", "run not found")
		return
	}
	limit := message.Limit
	if limit <= 0 || limit > 1000 {
		limit = 300
	}
	steps, err := s.store.ListRunSteps(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "dash run subscribe steps failed", "dash_id", dash.ID, "agent_id", agentID, "run_id", runID, "error", err)
		s.sendDashError(dash, "run_snapshot_failed", "unable to load run timeline")
		return
	}
	messages, err := s.store.ListRunMessages(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "dash run subscribe messages failed", "dash_id", dash.ID, "agent_id", agentID, "run_id", runID, "error", err)
		s.sendDashError(dash, "run_snapshot_failed", "unable to load run messages")
		return
	}
	tools, err := s.store.ListToolCalls(r.Context(), agentID, runID)
	if err != nil {
		s.log.ErrorContext(r.Context(), "dash run subscribe tools failed", "dash_id", dash.ID, "agent_id", agentID, "run_id", runID, "error", err)
		s.sendDashError(dash, "run_snapshot_failed", "unable to load tool calls")
		return
	}
	events, err := s.store.ListRunEvents(r.Context(), agentID, runID, message.AfterSeq, limit)
	if err != nil {
		s.log.ErrorContext(r.Context(), "dash run subscribe events failed", "dash_id", dash.ID, "agent_id", agentID, "run_id", runID, "error", err)
		s.sendDashError(dash, "run_snapshot_failed", "unable to load run events")
		return
	}
	dash.mu.Lock()
	dash.Subscriptions[dashSubscriptionKey(agentID, runID)] = DashSubscription{AgentID: agentID, RunID: runID}
	dash.mu.Unlock()
	s.sendDash(dash, "run.snapshot", map[string]any{
		"agent_id":   agentID,
		"run_id":     runID,
		"after_seq":  message.AfterSeq,
		"events":     events,
		"timeline":   steps,
		"messages":   messages,
		"tool_calls": tools,
	})
}

func (s *Server) authorizeDashWSAgent(r *http.Request, dash *DashConn, agentID string) (DashPrincipal, string, bool) {
	identity, err := s.store.AgentByID(r.Context(), agentID)
	if err != nil {
		return DashPrincipal{}, "", false
	}
	if !dash.Principal.Admin && identity.UserID != dash.Principal.UserID {
		return DashPrincipal{}, "", false
	}
	return dash.Principal, identity.AgentID, true
}

func (s *Server) sendDash(dash *DashConn, eventType string, payload any) {
	if dash == nil {
		return
	}
	wire := map[string]any{
		"type":    eventType,
		"payload": payload,
	}
	if payloadMap, ok := payload.(map[string]any); ok {
		if agentID, ok := payloadMap["agent_id"].(string); ok && agentID != "" {
			wire["agent_id"] = agentID
		}
		if runID, ok := payloadMap["run_id"].(string); ok && runID != "" {
			wire["run_id"] = runID
		}
	}
	data := protocol.Raw(wire)
	select {
	case dash.Send <- data:
	default:
	}
}

func (s *Server) sendDashError(dash *DashConn, code, message string) {
	s.sendDash(dash, "error", map[string]string{"code": code, "message": message})
}

func dashSubscriptionKey(agentID, runID string) string {
	return strings.TrimSpace(agentID) + "\x00" + strings.TrimSpace(runID)
}
