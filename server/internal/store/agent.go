package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) AgentBySecret(ctx context.Context, secret string) (AgentIdentity, error) {
	var identity AgentIdentity
	var capabilities []byte
	err := s.pool.QueryRow(ctx, `SELECT id, workspace_id, user_id, device_id, agent_secret, server_key_id, COALESCE(capabilities, '{}'::jsonb) FROM agents WHERE agent_secret=$1`, secret).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID, &capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("invalid agent token")
	}
	if len(capabilities) > 0 {
		identity.Capabilities = append(identity.Capabilities, capabilities...)
	}
	return identity, err
}

func (s *Store) AgentByID(ctx context.Context, agentID string) (AgentIdentity, error) {
	var identity AgentIdentity
	var capabilities []byte
	err := s.pool.QueryRow(ctx, `SELECT id, workspace_id, user_id, device_id, agent_secret, server_key_id, COALESCE(capabilities, '{}'::jsonb) FROM agents WHERE id=$1`, agentID).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID, &capabilities)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("agent not found")
	}
	if len(capabilities) > 0 {
		identity.Capabilities = append(identity.Capabilities, capabilities...)
	}
	return identity, err
}

func (s *Store) SaveAgentRegister(ctx context.Context, register protocol.AgentRegister) error {
	_, err := s.pool.Exec(ctx, `UPDATE devices SET hostname=$1, os=$2, arch=$3, username_hash=$4, agent_version=$5, install_state=$6, last_seen_at=now() WHERE id=$7`,
		register.Device.Hostname, register.Device.OS, register.Device.Arch, register.Device.UsernameHash, register.Device.AgentVersion, jsonRaw(register.Device.InstallState), register.DeviceID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE agents SET protocol_version=$1, capabilities=$2, credential=$3, registered_at=now(), last_seen_at=now() WHERE id=$4`,
		protocol.Version, jsonRaw(register.Capabilities), jsonRaw(register.Credential), register.AgentID)
	return err
}

func (s *Store) TouchAgent(ctx context.Context, agentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen_at=now() WHERE id=$1`, agentID)
	return err
}
