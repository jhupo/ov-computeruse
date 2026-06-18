package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

var ErrSessionActive = errors.New("session already has an active run")

type AgentIdentity struct {
	AgentID              string          `json:"agent_id"`
	WorkspaceID          string          `json:"workspace_id"`
	UserID               string          `json:"-"`
	UserDisabledAt       time.Time       `json:"user_disabled_at,omitempty"`
	UserDisabledReason   string          `json:"user_disabled_reason,omitempty"`
	DeviceID             string          `json:"device_id"`
	AgentSecret          string          `json:"-"`
	ServerURL            string          `json:"server_url"`
	ServerKeyID          string          `json:"server_key_id"`
	Capabilities         json.RawMessage `json:"capabilities,omitempty"`
	Credential           json.RawMessage `json:"credential,omitempty"`
	DisabledAt           time.Time       `json:"agent_disabled_at,omitempty"`
	DeviceDisabledAt     time.Time       `json:"device_disabled_at,omitempty"`
	DisabledReason       string          `json:"disabled_reason,omitempty"`
	DeviceDisabledReason string          `json:"device_disabled_reason,omitempty"`
}

type UserIdentity struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

func (a AgentIdentity) AccessError() error {
	if !a.UserDisabledAt.IsZero() {
		if a.UserDisabledReason != "" {
			return errors.New("user is disabled: " + a.UserDisabledReason)
		}
		return errors.New("user is disabled")
	}
	if !a.DisabledAt.IsZero() {
		if a.DisabledReason != "" {
			return errors.New("agent is disabled: " + a.DisabledReason)
		}
		return errors.New("agent is disabled")
	}
	if !a.DeviceDisabledAt.IsZero() {
		if a.DeviceDisabledReason != "" {
			return errors.New("device is disabled: " + a.DeviceDisabledReason)
		}
		return errors.New("device is disabled")
	}
	return nil
}

func New(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	s := &Store{pool: pool}
	if err := s.migrate(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() {
	s.pool.Close()
}

func jsonRaw(value any) []byte {
	if raw, ok := value.([]byte); ok {
		return raw
	}
	raw, _ := json.Marshal(value)
	return raw
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func now() time.Time {
	return time.Now().UTC()
}
