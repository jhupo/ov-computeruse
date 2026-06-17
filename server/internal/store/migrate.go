package store

import "context"

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, balance_cents BIGINT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS user_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), base_url TEXT NOT NULL, key_fingerprint TEXT NOT NULL, balance_cents BIGINT NOT NULL DEFAULT 0, disabled_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), install_id TEXT NOT NULL, machine_hash TEXT NOT NULL, hostname TEXT, os TEXT, arch TEXT, username_hash TEXT, agent_version TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS agents (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), device_id TEXT NOT NULL UNIQUE REFERENCES devices(id), agent_secret TEXT NOT NULL, server_key_id TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS codex_roots (agent_id TEXT NOT NULL REFERENCES agents(id), path TEXT NOT NULL, kind TEXT, source TEXT, exists BOOLEAN NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, path))`,
		`CREATE TABLE IF NOT EXISTS projects (agent_id TEXT NOT NULL REFERENCES agents(id), id TEXT NOT NULL, name TEXT, path TEXT, last_active_at TIMESTAMPTZ, has_agents_md BOOLEAN NOT NULL DEFAULT false, git_branch TEXT, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, id))`,
		`CREATE TABLE IF NOT EXISTS codex_sessions (agent_id TEXT NOT NULL REFERENCES agents(id), id TEXT NOT NULL, id_source TEXT, project_id TEXT, title TEXT, path TEXT, cwd TEXT, updated_at TIMESTAMPTZ, size_bytes BIGINT, content_sha256 TEXT, indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, id))`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS id_source TEXT`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS cwd TEXT`,
		`CREATE TABLE IF NOT EXISTS history_chunks (agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, chunk_index INTEGER NOT NULL, sha256 TEXT NOT NULL, size_bytes BIGINT NOT NULL, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, session_id, chunk_index, sha256))`,
		`CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), project_id TEXT, session_id TEXT, status TEXT NOT NULL, started_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS run_events (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), device_id TEXT NOT NULL, run_id TEXT, session_id TEXT, project_id TEXT, kind TEXT NOT NULL, payload JSONB, event_at TIMESTAMPTZ NOT NULL, received_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS commands (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT, session_id TEXT, project_id TEXT, kind TEXT NOT NULL, payload JSONB, status TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), acked_at TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS heartbeats (agent_id TEXT PRIMARY KEY REFERENCES agents(id), device_id TEXT NOT NULL, status TEXT NOT NULL, payload JSONB NOT NULL, received_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS approval_requests (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT, project_id TEXT, session_id TEXT, category TEXT, action TEXT, risk_level TEXT, payload JSONB, status TEXT NOT NULL, requested_at TIMESTAMPTZ NOT NULL DEFAULT now(), decided_at TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (id TEXT PRIMARY KEY, user_id TEXT, agent_id TEXT, action TEXT NOT NULL, payload JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_device ON agents(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_install ON devices(user_id, install_id, machine_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_agent ON projects(agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_agent ON codex_sessions(agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id, received_at)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_agent ON run_events(agent_id, received_at)`,
		`CREATE INDEX IF NOT EXISTS idx_commands_agent_status ON commands(agent_id, status, created_at)`,
	}
	for _, stmt := range indexes {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
