package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool *pgxpool.Pool
}

type AgentIdentity struct {
	AgentID      string          `json:"agent_id"`
	WorkspaceID  string          `json:"workspace_id"`
	UserID       string          `json:"-"`
	DeviceID     string          `json:"device_id"`
	AgentSecret  string          `json:"agent_secret"`
	ServerURL    string          `json:"server_url"`
	ServerKeyID  string          `json:"server_key_id"`
	Capabilities json.RawMessage `json:"capabilities,omitempty"`
	Credential   json.RawMessage `json:"credential,omitempty"`
}

type UserIdentity struct {
	UserID       string `json:"user_id"`
	Username     string `json:"username"`
	BalanceCents int64  `json:"balance_cents"`
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
