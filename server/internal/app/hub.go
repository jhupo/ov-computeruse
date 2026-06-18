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
	AgentID      string
	UserID       string
	DeviceID     string
	Secret       string
	Epoch        int64
	ConnectionID string
	Conn         *websocket.Conn
	Send         chan []byte
	ConnectedAt  time.Time
	Replay       *protocol.ReplayGuard
}

type DashConn struct {
	ID            string
	Principal     DashPrincipal
	Conn          *websocket.Conn
	Send          chan []byte
	mu            sync.RWMutex
	Subscriptions map[string]DashSubscription
}

type DashSubscription struct {
	AgentID  string
	RunID    string
	AfterSeq uint64
}

type AgentCommandEnvelope struct {
	Origin       string `json:"origin"`
	AgentID      string `json:"agent_id"`
	UserID       string `json:"user_id"`
	CommandID    string `json:"command_id,omitempty"`
	ConnectionID string `json:"connection_id,omitempty"`
	Epoch        int64  `json:"epoch,omitempty"`
	Data         []byte `json:"data"`
}

type CommandDispatchStatus string

const (
	CommandDispatchDelivered   CommandDispatchStatus = "delivered"
	CommandDispatchDelegated   CommandDispatchStatus = "delegated"
	CommandDispatchUnavailable CommandDispatchStatus = "unavailable"
	CommandDispatchQueueFull   CommandDispatchStatus = "queue_full"
)

type DashBroadcastEnvelope struct {
	Origin string `json:"origin"`
	UserID string `json:"user_id"`
	Data   []byte `json:"data"`
}

type WorkspaceResponseEnvelope struct {
	Origin   string                     `json:"origin"`
	Response protocol.WorkspaceResponse `json:"response"`
}

type AgentDisconnectEnvelope struct {
	Origin   string `json:"origin"`
	AgentID  string `json:"agent_id"`
	UserID   string `json:"user_id,omitempty"`
	Reason   string `json:"reason,omitempty"`
	MaxEpoch int64  `json:"max_epoch,omitempty"`
}

type AgentLease struct {
	AgentID      string    `json:"agent_id"`
	UserID       string    `json:"user_id"`
	DeviceID     string    `json:"device_id"`
	InstanceID   string    `json:"instance_id"`
	ConnectionID string    `json:"connection_id"`
	Epoch        int64     `json:"epoch"`
	UpdatedAt    time.Time `json:"updated_at"`
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

func (h *Hub) InstanceID() string {
	return h.instanceID
}

func (h *Hub) Run(ctx context.Context) {
	go h.subscribeDash(ctx)
	go h.subscribeCommands(ctx)
	go h.subscribeAgentDisconnects(ctx)
}

func (h *Hub) AddAgent(ctx context.Context, conn *AgentConn) {
	h.mu.Lock()
	previous := h.agents[conn.AgentID]
	h.agents[conn.AgentID] = conn
	h.mu.Unlock()
	if previous != nil && previous != conn && previous.Conn != nil {
		_ = previous.Conn.Close()
	}
	_ = h.TouchAgent(ctx, conn)
}

func (h *Hub) RemoveAgent(ctx context.Context, agentID string) {
	h.mu.Lock()
	delete(h.agents, agentID)
	h.mu.Unlock()
	h.removeAgentLease(ctx, agentID)
}

func (h *Hub) RemoveAgentConn(ctx context.Context, conn *AgentConn) {
	if conn == nil {
		return
	}
	removed := false
	h.mu.Lock()
	current := h.agents[conn.AgentID]
	if current == conn {
		delete(h.agents, conn.AgentID)
		removed = true
	}
	h.mu.Unlock()
	if removed {
		h.removeAgentLeaseIfOwned(ctx, conn)
	}
}

func (h *Hub) DisconnectAgent(ctx context.Context, agentID string) {
	h.DisconnectAgentBeforeEpoch(ctx, agentID, 0)
}

func (h *Hub) DisconnectAgentBeforeEpoch(ctx context.Context, agentID string, maxEpoch int64) {
	h.disconnectAgentLocal(ctx, agentID, maxEpoch)
	if h.redis == nil {
		return
	}
	raw, err := json.Marshal(AgentDisconnectEnvelope{Origin: h.instanceID, AgentID: agentID, Reason: "access_changed", MaxEpoch: maxEpoch})
	if err != nil {
		return
	}
	_ = h.redis.Publish(ctx, "ov:agent:disconnects", raw).Err()
}

func (h *Hub) DisconnectUserAgents(ctx context.Context, userID string) {
	h.disconnectUserAgentsLocal(ctx, userID)
	if h.redis == nil {
		return
	}
	raw, err := json.Marshal(AgentDisconnectEnvelope{Origin: h.instanceID, UserID: userID, Reason: "user_access_changed"})
	if err != nil {
		return
	}
	_ = h.redis.Publish(ctx, "ov:agent:disconnects", raw).Err()
}

func (h *Hub) disconnectAgentLocal(ctx context.Context, agentID string, maxEpoch int64) {
	h.mu.Lock()
	conn := h.agents[agentID]
	if conn != nil && maxEpoch > 0 && conn.Epoch > maxEpoch {
		conn = nil
	} else {
		delete(h.agents, agentID)
	}
	h.mu.Unlock()
	if maxEpoch > 0 && conn != nil {
		h.removeAgentLeaseIfOwned(ctx, conn)
	} else if maxEpoch == 0 {
		h.removeAgentLease(ctx, agentID)
	}
	if conn != nil && conn.Conn != nil {
		_ = conn.Conn.Close()
	}
}

func (h *Hub) disconnectUserAgentsLocal(ctx context.Context, userID string) {
	h.mu.Lock()
	conns := []*AgentConn{}
	for agentID, conn := range h.agents {
		if conn.UserID != userID {
			continue
		}
		conns = append(conns, conn)
		delete(h.agents, agentID)
	}
	h.mu.Unlock()
	for _, conn := range conns {
		h.removeAgentLease(ctx, conn.AgentID)
		if conn.Conn != nil {
			_ = conn.Conn.Close()
		}
	}
}

func (h *Hub) removeAgentLease(ctx context.Context, agentID string) {
	if h.redis == nil {
		return
	}
	_ = h.redis.Del(ctx, "agent:online:"+agentID).Err()
}

func (h *Hub) removeAgentLeaseIfOwned(ctx context.Context, conn *AgentConn) {
	if h.redis == nil || conn == nil {
		return
	}
	key := "agent:online:" + conn.AgentID
	raw, err := h.redis.Get(ctx, key).Bytes()
	if err != nil {
		return
	}
	var lease AgentLease
	if json.Unmarshal(raw, &lease) != nil {
		return
	}
	if lease.InstanceID == h.instanceID && lease.ConnectionID == conn.ConnectionID && lease.Epoch == conn.Epoch {
		_ = h.redis.Del(ctx, key).Err()
	}
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
	raw, err := json.Marshal(AgentLease{
		AgentID:      conn.AgentID,
		UserID:       conn.UserID,
		DeviceID:     conn.DeviceID,
		InstanceID:   h.instanceID,
		ConnectionID: conn.ConnectionID,
		Epoch:        conn.Epoch,
		UpdatedAt:    time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return h.redis.Set(ctx, "agent:online:"+conn.AgentID, raw, 90*time.Second).Err()
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

func (h *Hub) DispatchCommand(ctx context.Context, agentID, userID, commandID string, data []byte) CommandDispatchStatus {
	status := h.dispatchEnvelope(ctx, agentID, userID, commandID, data)
	if status == CommandDispatchDelivered {
		h.markCommandDispatched(ctx, agentID, commandID)
	}
	return status
}

func (h *Hub) DispatchEnvelope(ctx context.Context, agentID, userID string, data []byte) CommandDispatchStatus {
	return h.dispatchEnvelope(ctx, agentID, userID, "", data)
}

func (h *Hub) dispatchEnvelope(ctx context.Context, agentID, userID, commandID string, data []byte) CommandDispatchStatus {
	h.mu.RLock()
	agent := h.agents[agentID]
	h.mu.RUnlock()
	if agent != nil {
		if agent.UserID != userID {
			return CommandDispatchUnavailable
		}
		if h.connOwnsLease(ctx, agent) {
			status := h.dispatchCommandLocal(agentID, userID, agent.ConnectionID, agent.Epoch, data)
			return status
		}
	}
	if h.redis == nil {
		return CommandDispatchUnavailable
	}
	lease, ok := h.agentLease(ctx, agentID)
	if !ok || lease.UserID != userID {
		return CommandDispatchUnavailable
	}
	if lease.InstanceID == h.instanceID {
		return h.dispatchCommandLocal(agentID, userID, lease.ConnectionID, lease.Epoch, data)
	}
	raw, err := json.Marshal(AgentCommandEnvelope{Origin: h.instanceID, AgentID: agentID, UserID: userID, CommandID: commandID, ConnectionID: lease.ConnectionID, Epoch: lease.Epoch, Data: data})
	if err != nil {
		return CommandDispatchUnavailable
	}
	if err := h.redis.Publish(ctx, "ov:agent:commands", raw).Err(); err != nil {
		return CommandDispatchUnavailable
	}
	return CommandDispatchDelegated
}

func (h *Hub) connOwnsLease(ctx context.Context, conn *AgentConn) bool {
	if h.redis == nil || conn == nil {
		return true
	}
	lease, ok := h.agentLease(ctx, conn.AgentID)
	if !ok {
		return false
	}
	return lease.InstanceID == h.instanceID && lease.ConnectionID == conn.ConnectionID && lease.Epoch == conn.Epoch
}

func (h *Hub) agentLease(ctx context.Context, agentID string) (AgentLease, bool) {
	if h.redis == nil {
		return AgentLease{}, false
	}
	raw, err := h.redis.Get(ctx, "agent:online:"+agentID).Bytes()
	if err != nil {
		return AgentLease{}, false
	}
	var lease AgentLease
	if json.Unmarshal(raw, &lease) != nil {
		return AgentLease{}, false
	}
	return lease, true
}

func (h *Hub) broadcastDashLocal(userID string, data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, dash := range h.dashes {
		if !dash.Principal.Admin && dash.Principal.UserID != userID {
			continue
		}
		if !dashAcceptsBroadcast(dash, data) {
			continue
		}
		select {
		case dash.Send <- data:
		default:
		}
	}
}

func dashAcceptsBroadcast(dash *DashConn, data []byte) bool {
	if dash == nil {
		return true
	}
	var event struct {
		Type    string `json:"type"`
		AgentID string `json:"agent_id"`
		Payload struct {
			RunID string `json:"run_id"`
			Seq   uint64 `json:"seq"`
		} `json:"payload"`
	}
	if json.Unmarshal(data, &event) != nil {
		return true
	}
	if event.Type != "run.event" {
		return true
	}
	dash.mu.RLock()
	subscriptions := make([]DashSubscription, 0, len(dash.Subscriptions))
	for _, subscription := range dash.Subscriptions {
		subscriptions = append(subscriptions, subscription)
	}
	dash.mu.RUnlock()
	for _, subscription := range subscriptions {
		if subscription.AgentID == event.AgentID && subscription.RunID == event.Payload.RunID && event.Payload.Seq > subscription.AfterSeq {
			return true
		}
	}
	return false
}

func (h *Hub) dispatchCommandLocal(agentID, userID, connectionID string, epoch int64, data []byte) CommandDispatchStatus {
	h.mu.RLock()
	agent := h.agents[agentID]
	h.mu.RUnlock()
	if agent == nil {
		return CommandDispatchUnavailable
	}
	if agent.UserID != userID {
		return CommandDispatchUnavailable
	}
	if connectionID != "" && agent.ConnectionID != connectionID {
		return CommandDispatchUnavailable
	}
	if epoch > 0 && agent.Epoch != epoch {
		return CommandDispatchUnavailable
	}
	select {
	case agent.Send <- data:
		return CommandDispatchDelivered
	default:
		return CommandDispatchQueueFull
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
		if h.dispatchCommandLocal(env.AgentID, env.UserID, env.ConnectionID, env.Epoch, env.Data) == CommandDispatchDelivered {
			h.markCommandDispatched(ctx, env.AgentID, env.CommandID)
		}
	}
}

func (h *Hub) subscribeAgentDisconnects(ctx context.Context) {
	if h.redis == nil {
		return
	}
	pubsub := h.redis.Subscribe(ctx, "ov:agent:disconnects")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var env AgentDisconnectEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
			h.log.WarnContext(ctx, "invalid agent disconnect envelope", "error", err)
			continue
		}
		if env.Origin == h.instanceID {
			continue
		}
		if env.UserID != "" {
			h.disconnectUserAgentsLocal(ctx, env.UserID)
			continue
		}
		if env.AgentID != "" {
			h.disconnectAgentLocal(ctx, env.AgentID, env.MaxEpoch)
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
