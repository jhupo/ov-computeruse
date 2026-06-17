package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

type BindUser struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	BaseURL     string `json:"base_url"`
	Fingerprint string `json:"fingerprint"`
	Balance     int64  `json:"balance_cents"`
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
	_, err = s.pool.Exec(ctx, `INSERT INTO users (id, username, password_hash, balance_cents) VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET username=EXCLUDED.username, password_hash=EXCLUDED.password_hash, balance_cents=EXCLUDED.balance_cents`, user.ID, user.Username, string(passwordHash), user.Balance)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO user_keys (id, user_id, base_url, key_fingerprint, balance_cents) VALUES ($1,$2,$3,$4,$5) ON CONFLICT (id) DO UPDATE SET base_url=EXCLUDED.base_url, key_fingerprint=EXCLUDED.key_fingerprint, balance_cents=EXCLUDED.balance_cents`, "key_"+user.ID, user.ID, user.BaseURL, user.Fingerprint, user.Balance)
	return err
}

func (s *Store) AuthenticateUser(ctx context.Context, username, password string) (UserIdentity, error) {
	var user UserIdentity
	var passwordHash string
	err := s.pool.QueryRow(ctx, `SELECT id, username, password_hash, balance_cents FROM users WHERE username=$1`, username).Scan(&user.UserID, &user.Username, &passwordHash, &user.BalanceCents)
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
		return AgentIdentity{}, err
	}
	if user.BalanceCents <= 0 {
		return AgentIdentity{}, errors.New("account balance is exhausted")
	}
	var keyID string
	err = s.pool.QueryRow(ctx, `SELECT id FROM user_keys WHERE user_id=$1 AND key_fingerprint=$2 AND disabled_at IS NULL LIMIT 1`, user.UserID, credential.Fingerprint).Scan(&keyID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentIdentity{}, errors.New("codex credential is not assigned to user")
	}
	if err != nil {
		return AgentIdentity{}, err
	}
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
	return AgentIdentity{AgentID: agentID, WorkspaceID: workspaceID, UserID: user.UserID, DeviceID: deviceID, AgentSecret: agentSecret, ServerURL: serverURL, ServerKeyID: serverKeyID}, nil
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
