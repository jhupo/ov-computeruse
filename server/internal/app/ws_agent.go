package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/security"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	identity, err := s.store.AgentBySecret(r.Context(), token)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	agent := &AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret, Conn: conn, Send: make(chan []byte, 64), ConnectedAt: time.Now().UTC()}
	s.hub.AddAgent(r.Context(), agent)
	_ = s.store.TouchAgent(r.Context(), identity.AgentID)
	s.log.InfoContext(r.Context(), "agent connected", "agent_id", agent.AgentID, "user_id", agent.UserID, "device_id", agent.DeviceID)
	go s.agentWriter(agent)
	s.agentReader(r, agent)
}

func (s *Server) agentWriter(agent *AgentConn) {
	for data := range agent.Send {
		if err := agent.Conn.WriteMessage(websocket.TextMessage, data); err != nil {
			return
		}
	}
}

func (s *Server) agentReader(r *http.Request, agent *AgentConn) {
	defer func() {
		s.hub.RemoveAgent(r.Context(), agent.AgentID)
		_ = agent.Conn.Close()
		close(agent.Send)
		s.log.InfoContext(r.Context(), "agent disconnected", "agent_id", agent.AgentID, "user_id", agent.UserID, "device_id", agent.DeviceID)
	}()
	for {
		var env protocol.Envelope
		if err := agent.Conn.ReadJSON(&env); err != nil {
			return
		}
		if env.Signature == "" || !security.Verify(agent.Secret, unsignedBytes(env), env.Signature) {
			s.log.WarnContext(r.Context(), "invalid agent envelope signature", "agent_id", agent.AgentID, "type", env.Type)
			continue
		}
		s.handleAgentEnvelope(r, agent, env)
	}
}

func (s *Server) handleAgentEnvelope(r *http.Request, agent *AgentConn, env protocol.Envelope) {
	ctx := r.Context()
	s.hub.BroadcastDash(agent.UserID, protocol.Raw(env))
	switch env.Type {
	case "agent.register":
		_ = s.store.TouchAgent(ctx, agent.AgentID)
	case "agent.heartbeat":
		heartbeat, err := protocol.Decode[protocol.Heartbeat](env.Data)
		if err == nil {
			_ = s.store.SaveHeartbeat(ctx, agent.AgentID, agent.DeviceID, heartbeat)
			_ = s.hub.TouchAgent(ctx, agent)
		}
	case "index.roots":
		index, err := protocol.Decode[protocol.RootIndex](env.Data)
		if err == nil {
			_ = s.store.SaveRoots(ctx, agent.AgentID, index.Roots)
		}
	case "index.projects":
		index, err := protocol.Decode[protocol.ProjectIndex](env.Data)
		if err == nil {
			_ = s.store.SaveProjects(ctx, agent.AgentID, index.Projects)
		}
	case "index.sessions":
		index, err := protocol.Decode[protocol.SessionIndex](env.Data)
		if err == nil {
			_ = s.store.SaveSessions(ctx, agent.AgentID, index.Sessions)
		}
	case "history.chunk":
		chunk, err := protocol.Decode[protocol.HistoryChunk](env.Data)
		if err == nil {
			if err := s.store.SaveHistoryChunk(ctx, agent.AgentID, chunk); err == nil {
				s.sendAgent(agent, "history.chunk.ack", protocol.HistoryChunkAck{SessionID: chunk.SessionID, Index: chunk.Index, SHA256: chunk.SHA256, Status: "acked"})
			}
		}
	case "run.event":
		event, err := protocol.Decode[protocol.RunEvent](env.Data)
		if err == nil {
			_ = s.store.SaveRunEvent(ctx, agent.AgentID, agent.DeviceID, event)
		}
	case "ack":
		ack, err := protocol.Decode[protocol.Ack](env.Data)
		if err == nil {
			_ = s.store.MarkCommandAck(ctx, agent.AgentID, ack)
		}
	}
}

func (s *Server) sendAgent(agent *AgentConn, messageType string, data any) {
	message := s.agentEnvelope(agent, messageType, data)
	if message == nil {
		return
	}
	select {
	case agent.Send <- message:
	default:
	}
}

func (s *Server) agentEnvelope(agent *AgentConn, messageType string, data any) []byte {
	env, err := protocol.NewEnvelope(messageType, agent.AgentID, agent.DeviceID, 0, data)
	if err != nil {
		return nil
	}
	env.Signature = security.Sign(agent.Secret, unsignedBytes(env))
	return protocol.Raw(env)
}

func unsignedBytes(env protocol.Envelope) []byte {
	env.Signature = ""
	body, _ := json.Marshal(env)
	var compact bytes.Buffer
	_ = json.Compact(&compact, body)
	return compact.Bytes()
}
