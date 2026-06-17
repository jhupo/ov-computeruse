package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (s *Store) AgentBySecret(ctx context.Context, secret string) (AgentIdentity, error) {
	var identity AgentIdentity
	err := s.pool.QueryRow(ctx, `SELECT id, workspace_id, user_id, device_id, agent_secret, server_key_id FROM agents WHERE agent_secret=$1`, secret).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("invalid agent token")
	}
	return identity, err
}

func (s *Store) AgentByID(ctx context.Context, agentID string) (AgentIdentity, error) {
	var identity AgentIdentity
	err := s.pool.QueryRow(ctx, `SELECT id, workspace_id, user_id, device_id, agent_secret, server_key_id FROM agents WHERE id=$1`, agentID).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("agent not found")
	}
	return identity, err
}

func (s *Store) TouchAgent(ctx context.Context, agentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen_at=now() WHERE id=$1`, agentID)
	return err
}
