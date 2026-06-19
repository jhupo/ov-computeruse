package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) AgentBySecret(ctx context.Context, secret string) (AgentIdentity, error) {
	identity, err := s.agentIdentityBy(ctx, `a.agent_secret=$1`, secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("invalid agent token")
	}
	if err != nil {
		return AgentIdentity{}, err
	}
	if err := identity.AccessError(); err != nil {
		return AgentIdentity{}, err
	}
	return identity, nil
}

func (s *Store) AgentByID(ctx context.Context, agentID string) (AgentIdentity, error) {
	identity, err := s.agentIdentityBy(ctx, `a.id=$1`, agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("agent not found")
	}
	return identity, err
}

func (s *Store) agentIdentityBy(ctx context.Context, predicate string, args ...any) (AgentIdentity, error) {
	var identity AgentIdentity
	var capabilities []byte
	var credential []byte
	var userDisabledAt sql.NullTime
	var agentDisabledAt sql.NullTime
	var deviceDisabledAt sql.NullTime
	err := s.pool.QueryRow(ctx, `SELECT a.id, a.workspace_id, a.user_id, COALESCE(u.disabled_reason, ''), u.disabled_at, a.device_id, a.agent_secret, COALESCE(a.agent_epoch, 1), a.server_key_id,
			COALESCE(a.capabilities, '{}'::jsonb), COALESCE(a.credential, '{}'::jsonb),
			a.disabled_at, COALESCE(a.disabled_reason, ''), d.disabled_at, COALESCE(d.disabled_reason, '')
		FROM agents a
		JOIN users u ON u.id = a.user_id
		JOIN devices d ON d.id = a.device_id
		WHERE `+predicate, args...).Scan(
		&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.UserDisabledReason, &userDisabledAt, &identity.DeviceID, &identity.AgentSecret, &identity.AgentEpoch, &identity.ServerKeyID,
		&capabilities, &credential, &agentDisabledAt, &identity.DisabledReason, &deviceDisabledAt, &identity.DeviceDisabledReason,
	)
	if len(capabilities) > 0 {
		identity.Capabilities = append(identity.Capabilities, capabilities...)
	}
	if len(credential) > 0 {
		identity.Credential = append(identity.Credential, credential...)
	}
	if userDisabledAt.Valid {
		identity.UserDisabledAt = userDisabledAt.Time
	}
	if agentDisabledAt.Valid {
		identity.DisabledAt = agentDisabledAt.Time
	}
	if deviceDisabledAt.Valid {
		identity.DeviceDisabledAt = deviceDisabledAt.Time
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
	_, err = s.pool.Exec(ctx, saveAgentRegisterSQL(),
		protocol.Version, jsonRaw(register.Capabilities), agentID)
	return err
}

func saveAgentRegisterSQL() string {
	return `UPDATE agents SET protocol_version=$1, capabilities=$2, registered_at=now(), last_seen_at=now() WHERE id=$3`
}

func (s *Store) TouchAgent(ctx context.Context, agentID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen_at=now() WHERE id=$1`, agentID)
	return err
}

func (s *Store) AgentEpochMatches(ctx context.Context, agentID string, epoch int64) (bool, error) {
	var matches bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM agents WHERE id=$1 AND agent_epoch=$2 AND disabled_at IS NULL)`, agentID, epoch).Scan(&matches)
	return matches, err
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
	baseURLFingerprint := strings.TrimSpace(credential.BaseURLFingerprint)
	if keyFingerprint == "" || baseURLFingerprint == "" {
		return errors.New("agent credential is incomplete")
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1
		FROM user_keys
		WHERE user_id=$1
			AND key_fingerprint=$2
			AND disabled_at IS NULL
			AND base_url_fingerprint=$3
	)`, identity.UserID, keyFingerprint, baseURLFingerprint).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("agent credential is not assigned to user")
	}
	return nil
}
