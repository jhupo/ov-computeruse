package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"ov-computeruse/server/internal/protocol"
)

type AgentConn struct {
	AgentID     string
	UserID      string
	DeviceID    string
	Secret      string
	Conn        *websocket.Conn
	Send        chan []byte
	ConnectedAt time.Time
	Replay      *protocol.ReplayGuard
}

type DashConn struct {
	ID        string
	Principal DashPrincipal
	Conn      *websocket.Conn
	Send      chan []byte
}

type AgentCommandEnvelope struct {
	Origin    string `json:"origin"`
	AgentID   string `json:"agent_id"`
	UserID    string `json:"user_id"`
	CommandID string `json:"command_id,omitempty"`
	Data      []byte `json:"data"`
}

type DashBroadcastEnvelope struct {
	Origin string `json:"origin"`
	UserID string `json:"user_id"`
	Data   []byte `json:"data"`
}

type Hub struct {
	instanceID string
	redis      *redis.Client
	commands   EventRepository
	log        *slog.Logger
	mu         sync.RWMutex
	agents     map[string]*AgentConn
	dashes     map[string]*DashConn
}

func NewHub(redisClient *redis.Client, commands EventRepository, logger *slog.Logger) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	return &Hub{instanceID: randomHubInstanceID(), redis: redisClient, commands: commands, log: logger, agents: map[string]*AgentConn{}, dashes: map[string]*DashConn{}}
}

func (h *Hub) Run(ctx context.Context) {
	go h.subscribeDash(ctx)
	go h.subscribeCommands(ctx)
}

func (h *Hub) AddAgent(ctx context.Context, conn *AgentConn) {
	h.mu.Lock()
	h.agents[conn.AgentID] = conn
	h.mu.Unlock()
	_ = h.TouchAgent(ctx, conn)
}

func (h *Hub) RemoveAgent(ctx context.Context, agentID string) {
	h.mu.Lock()
	delete(h.agents, agentID)
	h.mu.Unlock()
	if h.redis == nil {
		return
	}
	_ = h.redis.Del(ctx, "agent:online:"+agentID).Err()
}

func (h *Hub) Agent(agentID string) (*AgentConn, bool) {
	h.mu.RLock()
	conn, ok := h.agents[agentID]
	h.mu.RUnlock()
	return conn, ok
}

func (h *Hub) AgentMayBeOnline(ctx context.Context, agentID string) bool {
	h.mu.RLock()
	_, ok := h.agents[agentID]
	h.mu.RUnlock()
	if ok {
		return true
	}
	if h.redis == nil {
		return false
	}
	return h.redis.Exists(ctx, "agent:online:"+agentID).Val() > 0
}

func (h *Hub) TouchAgent(ctx context.Context, conn *AgentConn) error {
	if h.redis == nil {
		return nil
	}
	return h.redis.Set(ctx, "agent:online:"+conn.AgentID, conn.DeviceID, 90*time.Second).Err()
}

func (h *Hub) AddDash(conn *DashConn) {
	h.mu.Lock()
	h.dashes[conn.ID] = conn
	h.mu.Unlock()
}

func (h *Hub) RemoveDash(id string) {
	h.mu.Lock()
	delete(h.dashes, id)
	h.mu.Unlock()
}

func (h *Hub) BroadcastDash(userID string, data []byte) {
	h.broadcastDashLocal(userID, data)
	if h.redis == nil {
		return
	}
	raw, err := json.Marshal(DashBroadcastEnvelope{Origin: h.instanceID, UserID: userID, Data: data})
	if err != nil {
		return
	}
	_ = h.redis.Publish(context.Background(), "ov:dash:broadcast", raw).Err()
}

func (h *Hub) DispatchCommand(ctx context.Context, agentID, userID, commandID string, data []byte) bool {
	h.mu.RLock()
	agent := h.agents[agentID]
	h.mu.RUnlock()
	if agent != nil {
		if agent.UserID != userID {
			return false
		}
		select {
		case agent.Send <- data:
			h.markCommandDispatched(ctx, agentID, commandID)
			return true
		default:
			return false
		}
	}
	if h.redis == nil {
		return false
	}
	if h.redis.Exists(ctx, "agent:online:"+agentID).Val() == 0 {
		return false
	}
	raw, err := json.Marshal(AgentCommandEnvelope{Origin: h.instanceID, AgentID: agentID, UserID: userID, CommandID: commandID, Data: data})
	if err != nil {
		return false
	}
	return h.redis.Publish(ctx, "ov:agent:commands", raw).Err() == nil
}

func (h *Hub) broadcastDashLocal(userID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, dash := range h.dashes {
		if !dash.Principal.Admin && dash.Principal.UserID != userID {
			continue
		}
		select {
		case dash.Send <- data:
		default:
		}
	}
}

func (h *Hub) dispatchCommandLocal(agentID string, data []byte) bool {
	h.mu.RLock()
	agent := h.agents[agentID]
	h.mu.RUnlock()
	if agent == nil {
		return false
	}
	select {
	case agent.Send <- data:
		return true
	default:
		return false
	}
}

func (h *Hub) subscribeDash(ctx context.Context) {
	if h.redis == nil {
		return
	}
	pubsub := h.redis.Subscribe(ctx, "ov:dash:broadcast")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var env DashBroadcastEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
			h.log.WarnContext(ctx, "invalid dash broadcast envelope", "error", err)
			continue
		}
		if env.Origin == h.instanceID {
			continue
		}
		h.broadcastDashLocal(env.UserID, env.Data)
	}
}

func (h *Hub) subscribeCommands(ctx context.Context) {
	if h.redis == nil {
		return
	}
	pubsub := h.redis.Subscribe(ctx, "ov:agent:commands")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var env AgentCommandEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
			h.log.WarnContext(ctx, "invalid agent command envelope", "error", err)
			continue
		}
		if env.Origin == h.instanceID {
			continue
		}
		h.mu.RLock()
		agent := h.agents[env.AgentID]
		h.mu.RUnlock()
		if agent == nil || agent.UserID != env.UserID {
			continue
		}
		if h.dispatchCommandLocal(env.AgentID, env.Data) {
			h.markCommandDispatched(ctx, env.AgentID, env.CommandID)
		}
	}
}

func (h *Hub) markCommandDispatched(ctx context.Context, agentID, commandID string) {
	if h.commands == nil || commandID == "" {
		return
	}
	if err := h.commands.MarkCommandDispatched(ctx, agentID, commandID); err != nil {
		h.log.WarnContext(ctx, "mark command dispatched failed", "agent_id", agentID, "command_id", commandID, "error", err)
	}
}

func randomHubInstanceID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(b[:])
}
