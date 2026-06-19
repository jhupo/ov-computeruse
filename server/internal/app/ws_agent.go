package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/security"
	"ov-computeruse/server/internal/store"
)

var upgrader = websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}

const (
	maxAgentEnvelopeBytes = 2 << 20
	envelopeClockSkew     = 5 * time.Minute
)

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	identity, err := s.store.AgentBySecret(r.Context(), token)
	if err != nil {
		s.log.WarnContext(r.Context(), "agent websocket rejected", "error", err)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	agent := &AgentConn{AgentID: identity.AgentID, UserID: identity.UserID, DeviceID: identity.DeviceID, Secret: identity.AgentSecret, Epoch: identity.AgentEpoch, ConnectionID: protocol.NewID("conn"), Conn: conn, Send: make(chan []byte, 64), ConnectedAt: time.Now().UTC(), Replay: protocol.NewReplayGuard(envelopeClockSkew * 2)}
	conn.SetReadLimit(maxAgentEnvelopeBytes)
	s.hub.AddAgent(r.Context(), agent)
	_ = s.store.TouchAgent(r.Context(), identity.AgentID)
	s.log.InfoContext(r.Context(), "agent connected", "agent_id", agent.AgentID, "user_id", agent.UserID, "device_id", agent.DeviceID, "epoch", agent.Epoch, "connection_id", agent.ConnectionID)
	go s.agentWriter(agent)
	go s.replayPendingCommands(r, identity)
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
		s.hub.RemoveAgentConn(r.Context(), agent)
		_ = agent.Conn.Close()
		close(agent.Send)
		s.log.InfoContext(r.Context(), "agent disconnected", "agent_id", agent.AgentID, "user_id", agent.UserID, "device_id", agent.DeviceID, "epoch", agent.Epoch, "connection_id", agent.ConnectionID)
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
		decrypted, err := protocol.DecryptEnvelopeData(agent.Secret, env)
		if err != nil {
			s.log.WarnContext(r.Context(), "invalid agent envelope encryption", "agent_id", agent.AgentID, "type", env.Type, "error", err)
			continue
		}
		env = decrypted
		if err := validateAgentEnvelope(agent, env); err != nil {
			s.log.WarnContext(r.Context(), "invalid agent envelope", "agent_id", agent.AgentID, "type", env.Type, "error", err)
			continue
		}
		if err := agent.Replay.Accept(env, time.Now().UTC()); err != nil {
			s.log.WarnContext(r.Context(), "replayed agent envelope rejected", "agent_id", agent.AgentID, "type", env.Type, "message_id", env.MessageID, "error", err)
			continue
		}
		matches, err := s.store.AgentEpochMatches(r.Context(), agent.AgentID, agent.Epoch)
		if err != nil || !matches {
			s.log.WarnContext(r.Context(), "stale agent connection fenced", "agent_id", agent.AgentID, "epoch", agent.Epoch, "connection_id", agent.ConnectionID, "error", err)
			return
		}
		s.handleAgentEnvelope(r, agent, env)
	}
}

func validateAgentEnvelope(agent *AgentConn, env protocol.Envelope) error {
	if env.Version != protocol.Version {
		return errors.New("unsupported protocol version")
	}
	if env.AgentID != "" && env.AgentID != agent.AgentID {
		return errors.New("agent id mismatch")
	}
	if env.DeviceID != "" && env.DeviceID != agent.DeviceID {
		return errors.New("device id mismatch")
	}
	if env.MessageID == "" || env.Nonce == "" || env.Type == "" {
		return errors.New("message id, nonce, and type are required")
	}
	now := time.Now().UTC()
	if env.Timestamp.IsZero() || env.Timestamp.Before(now.Add(-envelopeClockSkew)) || env.Timestamp.After(now.Add(envelopeClockSkew)) {
		return errors.New("message timestamp outside allowed window")
	}
	return nil
}

func (s *Server) handleAgentEnvelope(r *http.Request, agent *AgentConn, env protocol.Envelope) {
	ctx := r.Context()
	switch env.Type {
	case "agent.register":
		register, err := protocol.Decode[protocol.AgentRegister](env.Data)
		if err == nil {
			register.AgentID = agent.AgentID
			register.DeviceID = agent.DeviceID
			register.WorkspaceID = ""
			if err := s.store.SaveAgentRegister(ctx, register); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("agent.registered", agent, map[string]any{"capabilities": register.Capabilities, "credential": register.Credential}))
			}
		} else {
			_ = s.store.TouchAgent(ctx, agent.AgentID)
		}
	case "agent.heartbeat":
		heartbeat, err := protocol.Decode[protocol.Heartbeat](env.Data)
		if err == nil {
			if err := s.store.SaveHeartbeat(ctx, agent.AgentID, agent.DeviceID, heartbeat); err == nil {
				_ = s.hub.TouchAgent(ctx, agent)
				s.hub.BroadcastDash(agent.UserID, dashEvent("agent.heartbeat", agent, heartbeat))
			}
		}
	case "index.roots":
		index, err := protocol.Decode[protocol.RootIndex](env.Data)
		if err == nil {
			if err := s.store.SaveRoots(ctx, agent.AgentID, index.Roots); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("index.roots.updated", agent, map[string]any{"count": len(index.Roots)}))
			}
		}
	case "index.projects":
		index, err := protocol.Decode[protocol.ProjectIndex](env.Data)
		if err == nil {
			if err := s.store.SaveProjects(ctx, agent.AgentID, index.Projects); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("index.projects.updated", agent, map[string]any{"count": len(index.Projects)}))
			}
		}
	case "index.sessions":
		index, err := protocol.Decode[protocol.SessionIndex](env.Data)
		if err == nil {
			if err := s.store.SaveSessions(ctx, agent.AgentID, index.Sessions); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("index.sessions.updated", agent, map[string]any{"count": len(index.Sessions)}))
			}
		}
	case "index.runtime_sessions":
		index, err := protocol.Decode[protocol.RuntimeSessionIndex](env.Data)
		if err == nil {
			saved := 0
			for _, runtimeSession := range index.RuntimeSessions {
				if ok, err := s.store.UpsertRuntimeSession(ctx, agent.AgentID, runtimeSession); err == nil && ok {
					saved++
				}
			}
			s.hub.BroadcastDash(agent.UserID, dashEvent("index.runtime_sessions.updated", agent, map[string]any{"count": saved}))
		}
	case "index.deleted":
		deleted, err := protocol.Decode[protocol.DeletedIndex](env.Data)
		if err == nil {
			if err := s.store.MarkIndexDeleted(ctx, agent.AgentID, deleted); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("index.deleted", agent, map[string]any{"projects": len(deleted.Projects), "sessions": len(deleted.Sessions)}))
			}
		}
	case "history.chunk":
		chunk, err := protocol.Decode[protocol.HistoryChunk](env.Data)
		if err == nil {
			if err := s.store.SaveHistoryChunk(ctx, agent.AgentID, chunk); err == nil {
				s.sendAgent(agent, "history.chunk.ack", protocol.HistoryChunkAck{SessionID: chunk.SessionID, Index: chunk.Index, SHA256: chunk.SHA256, Status: "acked"})
			} else {
				s.log.WarnContext(ctx, "history chunk rejected", "agent_id", agent.AgentID, "session_id", chunk.SessionID, "index", chunk.Index, "error", err)
				s.sendAgent(agent, "history.chunk.ack", protocol.HistoryChunkAck{SessionID: chunk.SessionID, Index: chunk.Index, SHA256: chunk.SHA256, Status: "failed", Message: err.Error(), At: time.Now().UTC()})
			}
		}
	case "history.messages":
		messages, err := protocol.Decode[protocol.HistoryMessages](env.Data)
		if err == nil {
			if err := s.store.SaveHistoryMessages(ctx, agent.AgentID, messages); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("history.messages.updated", agent, map[string]any{"session_id": messages.SessionID, "count": len(messages.Messages)}))
			}
		}
	case "history.items":
		items, err := protocol.Decode[protocol.HistoryItems](env.Data)
		if err == nil {
			if err := s.store.SaveHistoryItems(ctx, agent.AgentID, items); err == nil {
				s.sendAgent(agent, "history.items.ack", protocol.HistoryItemsAck{SessionID: items.SessionID, Cursor: items.Cursor, UploadID: items.UploadID, BatchIndex: items.BatchIndex, Status: "acked", At: time.Now().UTC()})
				s.hub.BroadcastDash(agent.UserID, dashEvent("history.items.updated", agent, map[string]any{"session_id": items.SessionID, "count": countAcceptedHistoryItems(items), "cursor": items.Cursor, "reset": items.Reset, "upload_id": items.UploadID, "batch_index": items.BatchIndex, "batch_count": items.BatchCount, "final": items.Final}))
			} else {
				s.log.WarnContext(ctx, "history items rejected", "agent_id", agent.AgentID, "session_id", items.SessionID, "error", err)
				s.sendAgent(agent, "history.items.ack", protocol.HistoryItemsAck{SessionID: items.SessionID, Cursor: items.Cursor, UploadID: items.UploadID, BatchIndex: items.BatchIndex, Status: "failed", Message: err.Error(), At: time.Now().UTC()})
			}
		}
	case "sync.cursor":
		cursor, err := protocol.Decode[protocol.SyncCursor](env.Data)
		if err == nil {
			_ = s.store.SaveSyncCursor(ctx, agent.AgentID, cursor)
		}
	case "workspace.response":
		response, err := protocol.Decode[protocol.WorkspaceResponse](env.Data)
		if err == nil {
			s.workspace.Resolve(response)
		}
	case "workspace.git.updated":
		update, err := protocol.Decode[protocol.WorkspaceGitUpdated](env.Data)
		if err == nil {
			s.hub.BroadcastDash(agent.UserID, dashEvent("workspace.git.updated", agent, update))
		}
	case "run.event":
		event, err := protocol.Decode[protocol.RunEvent](env.Data)
		if err == nil {
			if skipAgentRunEvent(event) {
				if event.RunID != "" && event.Seq > 0 {
					s.sendAgent(agent, "run.event.ack", protocol.Ack{EventID: event.EventID, RunID: event.RunID, Status: "ignored", Message: "usage event ignored", AckSeq: event.Seq, At: time.Now().UTC()})
				}
				return
			}
			result, err := s.store.SaveRunEvent(ctx, agent.AgentID, agent.DeviceID, event)
			if err == nil {
				if event.RunID != "" && event.Seq > 0 {
					s.sendAgent(agent, "run.event.ack", protocol.Ack{EventID: event.EventID, RunID: event.RunID, Status: result.AckStatus(), AckSeq: event.Seq, At: time.Now().UTC()})
				}
				if result.ShouldBroadcast() {
					if runtimeSession, ok := runtimeSessionFromRunEvent(event); ok {
						s.hub.BroadcastDash(agent.UserID, dashEvent("runtime.session.updated", agent, runtimeSession))
					}
					s.hub.BroadcastDash(agent.UserID, dashEvent("run.event", agent, event))
				}
			}
		}
	case "ack":
		ack, err := protocol.Decode[protocol.Ack](env.Data)
		if err == nil {
			if err := s.store.MarkCommandAck(ctx, agent.AgentID, ack); err == nil {
				s.hub.BroadcastDash(agent.UserID, dashEvent("command.ack", agent, ack))
			}
		}
	}
}

func skipAgentRunEvent(event protocol.RunEvent) bool {
	return protocol.IsUsageKind(event.Kind)
}

func countAcceptedHistoryItems(batch protocol.HistoryItems) int {
	count := 0
	for _, item := range batch.Items {
		sessionID := item.SessionID
		if sessionID == "" {
			sessionID = batch.SessionID
		}
		if sessionID == "" || item.Kind == "" || protocol.IsUsageKind(item.Kind) {
			continue
		}
		count++
	}
	return count
}

func runtimeSessionFromRunEvent(event protocol.RunEvent) (protocol.RuntimeSession, bool) {
	switch event.Kind {
	case "session.created", "session.resumed", "session.updated":
	default:
		return protocol.RuntimeSession{}, false
	}
	runtimeSession, err := protocol.Decode[protocol.RuntimeSession](event.Payload)
	if err != nil {
		return protocol.RuntimeSession{}, false
	}
	if runtimeSession.Runtime != protocol.RuntimeCodexCLI {
		return protocol.RuntimeSession{}, false
	}
	if runtimeSession.SessionID == "" && runtimeSession.NativeSessionID == "" {
		return protocol.RuntimeSession{}, false
	}
	return runtimeSession, true
}

func dashEvent(eventType string, agent *AgentConn, payload any) []byte {
	return protocol.Raw(map[string]any{
		"type":      eventType,
		"agent_id":  agent.AgentID,
		"device_id": agent.DeviceID,
		"payload":   payload,
		"at":        time.Now().UTC(),
	})
}

func (s *Server) replayPendingCommands(r *http.Request, identity store.AgentIdentity) {
	commands, err := s.store.ClaimPendingCommands(r.Context(), identity.AgentID, s.hub.InstanceID(), 50)
	if err != nil {
		s.log.WarnContext(r.Context(), "pending command claim failed", "agent_id", identity.AgentID, "error", err)
		return
	}
	for _, command := range commands {
		record, dispatched := s.dispatchClaimedCommand(r.Context(), identity, command)
		s.log.InfoContext(r.Context(), "pending command replayed", "agent_id", identity.AgentID, "command_id", command.ID, "status", record.Status, "dispatched", dispatched)
	}
}

func (s *Server) dispatchClaimedCommand(ctx context.Context, identity store.AgentIdentity, command store.CommandRecord) (store.CommandRecord, bool) {
	if err := s.validateCommandTargets(ctx, identity.AgentID, command.ToProtocol()); err != nil {
		_ = s.store.MarkCommandFailed(ctx, identity.AgentID, command.ID, err.Error())
		record, _, _ := s.store.CommandByID(ctx, identity.AgentID, command.ID)
		s.log.WarnContext(ctx, "claimed command target invalid", "agent_id", identity.AgentID, "command_id", command.ID, "error", err)
		return record, false
	}
	return s.dispatchCommand(ctx, identity, command.ToProtocol())
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
	env, err = protocol.EncryptEnvelopeData(agent.Secret, env)
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
