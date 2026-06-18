package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/store"
)

const workspaceRequestTimeout = 15 * time.Second

type WorkspaceBroker struct {
	redis   *redis.Client
	hub     *Hub
	log     *slog.Logger
	mu      sync.Mutex
	pending map[string]chan protocol.WorkspaceResponse
}

type WorkspaceResponseEnvelope struct {
	Origin   string                     `json:"origin"`
	Response protocol.WorkspaceResponse `json:"response"`
}

func NewWorkspaceBroker(redisClient *redis.Client, hub *Hub, logger *slog.Logger) WorkspaceBroker {
	if logger == nil {
		logger = slog.Default()
	}
	return WorkspaceBroker{redis: redisClient, hub: hub, log: logger, pending: map[string]chan protocol.WorkspaceResponse{}}
}

func (b *WorkspaceBroker) Run(ctx context.Context) {
	if b == nil || b.redis == nil {
		return
	}
	pubsub := b.redis.Subscribe(ctx, "ov:workspace:responses")
	defer pubsub.Close()
	for msg := range pubsub.Channel() {
		var env WorkspaceResponseEnvelope
		if err := json.Unmarshal([]byte(msg.Payload), &env); err != nil {
			b.log.WarnContext(ctx, "invalid workspace response envelope", "error", err)
			continue
		}
		if env.Origin == b.hub.InstanceID() {
			continue
		}
		b.resolveLocal(env.Response)
	}
}

func (b *WorkspaceBroker) Send(ctx context.Context, identity store.AgentIdentity, req protocol.WorkspaceRequest, message []byte) (protocol.WorkspaceResponse, int, error) {
	if b == nil || b.hub == nil {
		return protocol.WorkspaceResponse{}, http.StatusInternalServerError, errors.New("workspace broker is unavailable")
	}
	if !b.hub.AgentMayBeOnline(ctx, identity.AgentID) {
		return protocol.WorkspaceResponse{}, http.StatusConflict, errors.New("agent is offline")
	}
	waitCh := b.register(req.RequestID)
	defer b.unregister(req.RequestID)
	if status := b.hub.DispatchEnvelope(ctx, identity.AgentID, identity.UserID, message); !workspaceDispatchAccepted(status) {
		return protocol.WorkspaceResponse{}, http.StatusConflict, errors.New("agent is not available")
	}
	timer := time.NewTimer(workspaceRequestTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return protocol.WorkspaceResponse{}, http.StatusRequestTimeout, ctx.Err()
	case <-timer.C:
		return protocol.WorkspaceResponse{}, http.StatusGatewayTimeout, errors.New("workspace request timed out")
	case resp := <-waitCh:
		if resp.RequestID != req.RequestID {
			return protocol.WorkspaceResponse{}, http.StatusBadGateway, errors.New("workspace response request_id mismatch")
		}
		return resp, http.StatusOK, nil
	}
}

func workspaceDispatchAccepted(status CommandDispatchStatus) bool {
	switch status {
	case CommandDispatchDelivered, CommandDispatchDelegated:
		return true
	default:
		return false
	}
}

func (b *WorkspaceBroker) Resolve(resp protocol.WorkspaceResponse) {
	if b == nil || strings.TrimSpace(resp.RequestID) == "" {
		return
	}
	if b.resolveLocal(resp) {
		return
	}
	b.publish(resp)
}

func (b *WorkspaceBroker) register(requestID string) chan protocol.WorkspaceResponse {
	ch := make(chan protocol.WorkspaceResponse, 1)
	b.mu.Lock()
	b.pending[requestID] = ch
	b.mu.Unlock()
	return ch
}

func (b *WorkspaceBroker) unregister(requestID string) {
	b.mu.Lock()
	delete(b.pending, requestID)
	b.mu.Unlock()
}

func (b *WorkspaceBroker) resolveLocal(resp protocol.WorkspaceResponse) bool {
	b.mu.Lock()
	ch := b.pending[resp.RequestID]
	b.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- resp:
	default:
	}
	return true
}

func (b *WorkspaceBroker) publish(resp protocol.WorkspaceResponse) {
	if b.redis == nil {
		return
	}
	raw, err := json.Marshal(WorkspaceResponseEnvelope{Origin: b.hub.InstanceID(), Response: resp})
	if err != nil {
		return
	}
	_ = b.redis.Publish(context.Background(), "ov:workspace:responses", raw).Err()
}
