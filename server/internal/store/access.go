package store

import (
	"context"
	"errors"
	"strings"
)

var ErrDeviceDisabled = errors.New("device is disabled")

type AccessChange struct {
	Enabled bool
	Reason  string
	Actor   string
}

func (s *Store) SetAgentAccess(ctx context.Context, agentID string, change AccessChange) (AgentIdentity, error) {
	reason := strings.TrimSpace(change.Reason)
	actor := strings.TrimSpace(change.Actor)
	if change.Enabled {
		_, err := s.pool.Exec(ctx, `UPDATE agents SET disabled_at=NULL, disabled_reason=NULL, disabled_by=NULL WHERE id=$1`, agentID)
		if err != nil {
			return AgentIdentity{}, err
		}
		return s.AgentByID(ctx, agentID)
	}
	_, err := s.pool.Exec(ctx, `UPDATE agents SET disabled_at=now(), disabled_reason=$2, disabled_by=$3 WHERE id=$1`, agentID, nullString(reason), nullString(actor))
	if err != nil {
		return AgentIdentity{}, err
	}
	return s.AgentByID(ctx, agentID)
}

func (s *Store) SetDeviceAccess(ctx context.Context, deviceID string, change AccessChange) error {
	reason := strings.TrimSpace(change.Reason)
	actor := strings.TrimSpace(change.Actor)
	if change.Enabled {
		_, err := s.pool.Exec(ctx, `UPDATE devices SET disabled_at=NULL, disabled_reason=NULL, disabled_by=NULL WHERE id=$1`, deviceID)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE devices SET disabled_at=now(), disabled_reason=$2, disabled_by=$3 WHERE id=$1`, deviceID, nullString(reason), nullString(actor))
	return err
}
