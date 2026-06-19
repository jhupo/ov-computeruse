package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"ov-computeruse/server/internal/security"
)

type BindUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	BaseURL     string `json:"base_url"`
	Fingerprint string `json:"fingerprint"`
	Name        string `json:"name,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Model       string `json:"model,omitempty"`
}

type DeviceProfile struct {
	InstallID    string `json:"install_id"`
	MachineHash  string `json:"machine_hash"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	UsernameHash string `json:"username_hash,omitempty"`
	AgentVersion string `json:"agent_version"`
}

type Credential struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Source      string `json:"source,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

type agentCredentialRecord struct {
	BaseURLFingerprint string `json:"base_url_fingerprint"`
	KeyFingerprint     string `json:"key_fingerprint"`
	Provider           string `json:"provider,omitempty"`
	Model              string `json:"model,omitempty"`
	Source             string `json:"source,omitempty"`
}

func (s *Store) EnsureBindUser(ctx context.Context, user BindUser) error {
	record, err := s.UpsertUser(ctx, UserUpsert{ID: user.ID, Username: user.Username, Password: user.Password, Actor: "bootstrap"})
	if err != nil {
		return err
	}
	_, err = s.UpsertUserKey(ctx, UserKeyUpsert{ID: "key_" + record.ID, UserID: record.ID, Name: user.Name, BaseURL: user.BaseURL, KeyFingerprint: user.Fingerprint, Provider: user.Provider, Model: user.Model, Actor: "bootstrap"})
	return err
}

func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (UserIdentity, error) {
	var user UserIdentity
	var passwordHash string
	var disabledAt sql.NullTime
	err := s.pool.QueryRow(ctx, `SELECT id, username, password_hash, disabled_at FROM users WHERE username=$1`, username).Scan(&user.UserID, &user.Username, &passwordHash, &disabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserIdentity{}, errors.New("invalid username or password")
	}
	if err != nil {
		return UserIdentity{}, err
	}
	if disabledAt.Valid {
		return UserIdentity{}, errors.New("user is disabled")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return UserIdentity{}, errors.New("invalid username or password")
	}
	return user, nil
}

func (s *Store) AuthenticateAndBind(ctx context.Context, username, password string, device DeviceProfile, credential Credential, serverURL string) (AgentIdentity, error) {
	user, err := s.AuthenticateUser(ctx, username, password)
	if err != nil {
		_ = s.SaveAuditLog(ctx, "", "", "agent.bind.rejected", map[string]any{"username": username, "reason": "invalid_credentials"})
		return AgentIdentity{}, err
	}
	if strings.TrimSpace(credential.Fingerprint) == "" || strings.TrimSpace(credential.BaseURL) == "" {
		_ = s.SaveAuditLog(ctx, user.UserID, "", "agent.bind.rejected", map[string]any{"username": username, "reason": "credential_incomplete"})
		return AgentIdentity{}, errors.New("codex credential is incomplete")
	}
	baseURL, err := normalizeBaseURL(credential.BaseURL)
	if err != nil {
		_ = s.SaveAuditLog(ctx, user.UserID, "", "agent.bind.rejected", map[string]any{"username": username, "reason": "invalid_base_url"})
		return AgentIdentity{}, errors.New("codex credential base_url is invalid")
	}
	baseURLFingerprint := security.FingerprintSecret(baseURL)
	var keyID string
	err = s.pool.QueryRow(ctx, `SELECT id FROM user_keys WHERE user_id=$1 AND key_fingerprint=$2 AND base_url_fingerprint=$3 AND disabled_at IS NULL LIMIT 1`, user.UserID, credential.Fingerprint, baseURLFingerprint).Scan(&keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = s.SaveAuditLog(ctx, user.UserID, "", "agent.bind.rejected", map[string]any{"username": username, "reason": "credential_not_assigned", "base_url_fingerprint": baseURLFingerprint, "key_fingerprint": credential.Fingerprint})
		return AgentIdentity{}, errors.New("codex credential is not assigned to user")
	}
	if err != nil {
		return AgentIdentity{}, err
	}
	deviceID, err := s.upsertDevice(ctx, user.UserID, device)
	if err != nil {
		if errors.Is(err, ErrDeviceDisabled) {
			_ = s.SaveAuditLog(ctx, user.UserID, "", "agent.bind.rejected", map[string]any{"username": username, "reason": "device_disabled", "install_id": device.InstallID, "machine_hash": device.MachineHash})
		}
		return AgentIdentity{}, err
	}
	agentID := "agt_" + randomHex(16)
	workspaceID := "wsp_" + user.UserID
	agentSecret := "sec_" + randomHex(32)
	var agentEpoch int64
	agentCredential := agentCredentialRecord{
		BaseURLFingerprint: baseURLFingerprint,
		KeyFingerprint:     credential.Fingerprint,
		Provider:           strings.TrimSpace(credential.Provider),
		Model:              strings.TrimSpace(credential.Model),
		Source:             strings.TrimSpace(credential.Source),
	}
	err = s.pool.QueryRow(ctx, `INSERT INTO agents (id, workspace_id, user_id, device_id, agent_secret, credential, agent_epoch, last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,1,now()) ON CONFLICT (device_id) DO UPDATE SET agent_secret=EXCLUDED.agent_secret, credential=EXCLUDED.credential, agent_epoch=agents.agent_epoch+1, last_seen_at=now() RETURNING id, agent_epoch`, agentID, workspaceID, user.UserID, deviceID, agentSecret, jsonRaw(agentCredential)).Scan(&agentID, &agentEpoch)
	if err != nil {
		return AgentIdentity{}, err
	}
	_ = s.SaveAuditLog(ctx, user.UserID, agentID, "agent.bind.accepted", map[string]any{"device_id": deviceID, "key_id": keyID, "base_url": baseURL, "credential_source": credential.Source})
	return AgentIdentity{AgentID: agentID, WorkspaceID: workspaceID, UserID: user.UserID, DeviceID: deviceID, AgentSecret: agentSecret, AgentEpoch: agentEpoch, ServerURL: serverURL}, nil
}

func (s *Store) SaveAuditLog(ctx context.Context, userID, agentID, action string, payload any) error {
	if action == "" {
		return nil
	}
	raw, _ := json.Marshal(payload)
	_, err := s.pool.Exec(ctx, `INSERT INTO audit_logs (id, user_id, agent_id, action, payload, created_at) VALUES ($1,$2,$3,$4,$5,now())`, "aud_"+randomHex(16), nullString(userID), nullString(agentID), action, raw)
	return err
}

func normalizeBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("base_url must be absolute")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (s *Store) upsertDevice(ctx context.Context, userID string, device DeviceProfile) (string, error) {
	var deviceID string
	var disabledAt sql.NullTime
	err := s.pool.QueryRow(ctx, `SELECT id, disabled_at FROM devices WHERE user_id=$1 AND install_id=$2 AND machine_hash=$3`, userID, device.InstallID, device.MachineHash).Scan(&deviceID, &disabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		deviceID = "dev_" + randomHex(16)
		_, err = s.pool.Exec(ctx, `INSERT INTO devices (id, user_id, install_id, machine_hash, hostname, os, arch, username_hash, agent_version, last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`, deviceID, userID, device.InstallID, device.MachineHash, device.Hostname, device.OS, device.Arch, device.UsernameHash, device.AgentVersion)
		return deviceID, err
	}
	if err != nil {
		return "", err
	}
	if disabledAt.Valid {
		return "", ErrDeviceDisabled
	}
	_, err = s.pool.Exec(ctx, `UPDATE devices SET hostname=$1, os=$2, arch=$3, username_hash=$4, agent_version=$5, last_seen_at=now() WHERE id=$6`, device.Hostname, device.OS, device.Arch, device.UsernameHash, device.AgentVersion, deviceID)
	return deviceID, err
}

func randomHex(size int) string {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return hex.EncodeToString([]byte(now().String()))
	}
	return hex.EncodeToString(b)
}
