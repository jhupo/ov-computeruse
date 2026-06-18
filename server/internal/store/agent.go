package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"ov-computeruse/server/internal/protocol"
	"ov-computeruse/server/internal/security"
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
	err := s.pool.QueryRow(ctx, `SELECT a.id, a.workspace_id, a.user_id, COALESCE(u.disabled_reason, ''), u.disabled_at, a.device_id, a.agent_secret, a.server_key_id,
			COALESCE(a.capabilities, '{}'::jsonb), COALESCE(a.credential, '{}'::jsonb),
			a.disabled_at, COALESCE(a.disabled_reason, ''), d.disabled_at, COALESCE(d.disabled_reason, '')
		FROM agents a
		JOIN users u ON u.id = a.user_id
		JOIN devices d ON d.id = a.device_id
		WHERE `+predicate, args...).Scan(
		&identity.AgentID, &identity.WorkspaceID, &identity.UserID, &identity.UserDisabledReason, &userDisabledAt, &identity.DeviceID, &identity.AgentSecret, &identity.ServerKeyID,
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
		if fallback, err := s.agentCredentialBaseURLFallback(ctx, identity.UserID, keyFingerprint, baseURLFingerprint); err != nil {
			return err
		} else if fallback {
			return nil
		}
		return errors.New("agent credential is not assigned to user")
	}
	return nil
}

func (s *Store) agentCredentialBaseURLFallback(ctx context.Context, userID, keyFingerprint, baseURLFingerprint string) (bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, base_url FROM user_keys WHERE user_id=$1 AND key_fingerprint=$2 AND disabled_at IS NULL AND COALESCE(base_url_fingerprint, '')=''`, userID, keyFingerprint)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var baseURL string
		if err := rows.Scan(&id, &baseURL); err != nil {
			return false, err
		}
		normalized, err := normalizeBaseURL(baseURL)
		if err != nil {
			continue
		}
		computed := security.FingerprintSecret(normalized)
		if computed != baseURLFingerprint {
			continue
		}
		_, _ = s.pool.Exec(ctx, `UPDATE user_keys SET base_url_fingerprint=$1 WHERE id=$2 AND COALESCE(base_url_fingerprint, '')=''`, computed, id)
		return true, nil
	}
	return false, rows.Err()
}
