package store

import (
	"context"
	"crypto/rand"
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

func (s *Store) EnsureBindUser(ctx context.Context, user BindUser) error {
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(user.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO users (id, username, password_hash) VALUES ($1,$2,$3) ON CONFLICT (id) DO UPDATE SET username=EXCLUDED.username, password_hash=EXCLUDED.password_hash`, user.ID, user.Username, string(passwordHash))
	if err != nil {
		return err
	}
	baseURL, err := normalizeBaseURL(user.BaseURL)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO user_keys (id, user_id, base_url, base_url_fingerprint, key_fingerprint)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (id) DO UPDATE SET base_url=EXCLUDED.base_url, base_url_fingerprint=EXCLUDED.base_url_fingerprint, key_fingerprint=EXCLUDED.key_fingerprint`,
		"key_"+user.ID, user.ID, baseURL, security.FingerprintSecret(baseURL), user.Fingerprint)
	return err
}

func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (UserIdentity, error) {
	var user UserIdentity
	var passwordHash string
	err := s.pool.QueryRow(ctx, `SELECT id, username, password_hash FROM users WHERE username=$1`, username).Scan(&user.UserID, &user.Username, &passwordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserIdentity{}, errors.New("invalid username or password")
	}
	if err != nil {
		return UserIdentity{}, err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)); err != nil {
		return UserIdentity{}, errors.New("invalid username or password")
	}
	return user, nil
}

func (s *Store) AuthenticateAndBind(ctx context.Context, username, password string, device DeviceProfile, credential Credential, serverURL, serverKeyID string) (AgentIdentity, error) {
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
	var keyID string
	err = s.pool.QueryRow(ctx, `SELECT id FROM user_keys WHERE user_id=$1 AND key_fingerprint=$2 AND lower(trim(trailing '/' from base_url))=$3 AND disabled_at IS NULL LIMIT 1`, user.UserID, credential.Fingerprint, baseURL).Scan(&keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = s.SaveAuditLog(ctx, user.UserID, "", "agent.bind.rejected", map[string]any{"username": username, "reason": "credential_not_assigned", "base_url": baseURL, "key_fingerprint": credential.Fingerprint})
		return AgentIdentity{}, errors.New("codex credential is not assigned to user")
	}
	if err != nil {
		return AgentIdentity{}, err
	}
	_, _ = s.pool.Exec(ctx, `UPDATE user_keys SET base_url_fingerprint=$1 WHERE id=$2 AND (base_url_fingerprint IS NULL OR base_url_fingerprint='')`, security.FingerprintSecret(baseURL), keyID)
	deviceID, err := s.upsertDevice(ctx, user.UserID, device)
	if err != nil {
		return AgentIdentity{}, err
	}
	agentID := "agt_" + randomHex(16)
	workspaceID := "wsp_" + user.UserID
	agentSecret := "sec_" + randomHex(32)
	err = s.pool.QueryRow(ctx, `INSERT INTO agents (id, workspace_id, user_id, device_id, agent_secret, server_key_id, last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,now()) ON CONFLICT (device_id) DO UPDATE SET agent_secret=EXCLUDED.agent_secret, server_key_id=EXCLUDED.server_key_id, last_seen_at=now() RETURNING id`, agentID, workspaceID, user.UserID, deviceID, agentSecret, serverKeyID).Scan(&agentID)
	if err != nil {
		return AgentIdentity{}, err
	}
	_ = s.SaveAuditLog(ctx, user.UserID, agentID, "agent.bind.accepted", map[string]any{"device_id": deviceID, "key_id": keyID, "base_url": baseURL, "credential_source": credential.Source})
	return AgentIdentity{AgentID: agentID, WorkspaceID: workspaceID, UserID: user.UserID, DeviceID: deviceID, AgentSecret: agentSecret, ServerURL: serverURL, ServerKeyID: serverKeyID}, nil
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
	err := s.pool.QueryRow(ctx, `SELECT id FROM devices WHERE user_id=$1 AND install_id=$2 AND machine_hash=$3`, userID, device.InstallID, device.MachineHash).Scan(&deviceID)
	if errors.Is(err, pgx.ErrNoRows) {
		deviceID = "dev_" + randomHex(16)
		_, err = s.pool.Exec(ctx, `INSERT INTO devices (id, user_id, install_id, machine_hash, hostname, os, arch, username_hash, agent_version, last_seen_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`, deviceID, userID, device.InstallID, device.MachineHash, device.Hostname, device.OS, device.Arch, device.UsernameHash, device.AgentVersion)
		return deviceID, err
	}
	if err != nil {
		return "", err
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
