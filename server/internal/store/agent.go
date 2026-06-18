package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) AgentBySecret(ctx context.Context, secret string) (AgentIdentity, error) {
	var identity AgentIdentity
	var capabilities []byte
	var credential []byte
	err := s.pool.QueryRow(ctx, `SELECT id, workspace_id, user_id, device_id, agent_secret, server_key_id, COALESCE(capabilities, '{}'::jsonb), COALESCE(credential, '{}'::jsonb) FROM agents WHERE agent_secret=$1`, secret).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID, &capabilities, &credential)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("invalid agent token")
	}
	if len(capabilities) > 0 {
		identity.Capabilities = append(identity.Capabilities, capabilities...)
	}
	if len(credential) > 0 {
		identity.Credential = append(identity.Credential, credential...)
	}
	return identity, err
}

func (s *Store) AgentByID(ctx context.Context, agentID string) (AgentIdentity, error) {
	var identity AgentIdentity
	var capabilities []byte
	var credential []byte
	err := s.pool.QueryRow(ctx, `SELECT id, workspace_id, user_id, device_id, agent_secret, server_key_id, COALESCE(capabilities, '{}'::jsonb), COALESCE(credential, '{}'::jsonb) FROM agents WHERE id=$1`, agentID).Scan(&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID, &capabilities, &credential)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("agent not found")
	}
	if len(capabilities) > 0 {
		identity.Capabilities = append(identity.Capabilities, capabilities...)
	}
	if len(credential) > 0 {
		identity.Credential = append(identity.Credential, credential...)
	}
	return identity, err
}

func (s *Store) SaveAgentRegister(ctx context.Context, register protocol.AgentRegister) error {
	agentID := strings.TrimSpace(register.AgentID)
	if agentID == "" {
		return errors.New("agent id required")
	}
	var deviceID string
	if err := s.pool.QueryRow(ctx, `SELECT device_id FROM agents WHERE id=$1`, agentID).Scan(&deviceID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("agent not found")
		}
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE devices SET hostname=$1, os=$2, arch=$3, username_hash=$4, agent_version=$5, install_state=$6, last_seen_at=now() WHERE id=$7`,
		register.Device.Hostname, register.Device.OS, register.Device.Arch, register.Device.UsernameHash, register.Device.AgentVersion, jsonRaw(register.Device.InstallState), deviceID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `UPDATE agents SET protocol_version=$1, capabilities=$2, credential=$3, registered_at=now(), last_seen_at=now() WHERE id=$4`,
		protocol.Version, jsonRaw(register.Capabilities), jsonRaw(register.Credential), agentID)
	return err
}

func (s *Store) TouchAgent(ctx context.Context, agentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen_at=now() WHERE id=$1`, agentID)
	return err
}

func (s *Store) AgentCredentialValid(ctx context.Context, identity AgentIdentity) error {
	if len(identity.Credential) == 0 {
		return errors.New("agent has not registered credential")
	}
	var credential struct {
		BaseURLFingerprint string `json:"base_url_fingerprint"`
		KeyFingerprint     string `json:"key_fingerprint"`
	}
	if err := json.Unmarshal(identity.Credential, &credential); err != nil {
		return errors.New("agent credential is invalid")
	}
	keyFingerprint := strings.TrimSpace(credential.KeyFingerprint)
	if keyFingerprint == "" {
		return errors.New("agent credential is incomplete")
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_keys WHERE user_id=$1 AND key_fingerprint=$2 AND disabled_at IS NULL)`, identity.UserID, keyFingerprint).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("agent credential is not assigned to user")
	}
	return nil
}
