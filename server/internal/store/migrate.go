package store

import "context"

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, balance_cents BIGINT NOT NULL DEFAULT 0, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS user_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), base_url TEXT NOT NULL, key_fingerprint TEXT NOT NULL, balance_cents BIGINT NOT NULL DEFAULT 0, disabled_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), install_id TEXT NOT NULL, machine_hash TEXT NOT NULL, hostname TEXT, os TEXT, arch TEXT, username_hash TEXT, agent_version TEXT, install_state JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ)`,
		`ALTER TABLE devices ADD COLUMN IF NOT EXISTS install_state JSONB`,
		`CREATE TABLE IF NOT EXISTS agents (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), device_id TEXT NOT NULL UNIQUE REFERENCES devices(id), agent_secret TEXT NOT NULL, server_key_id TEXT NOT NULL, protocol_version TEXT, capabilities JSONB, credential JSONB, registered_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ)`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS protocol_version TEXT`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS capabilities JSONB`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS credential JSONB`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS registered_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS codex_roots (agent_id TEXT NOT NULL REFERENCES agents(id), path TEXT NOT NULL, kind TEXT, source TEXT, exists BOOLEAN NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, path))`,
		`CREATE TABLE IF NOT EXISTS projects (agent_id TEXT NOT NULL REFERENCES agents(id), id TEXT NOT NULL, name TEXT, path TEXT, last_active_at TIMESTAMPTZ, has_agents_md BOOLEAN NOT NULL DEFAULT false, git_branch TEXT, deleted_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, id))`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS codex_sessions (agent_id TEXT NOT NULL REFERENCES agents(id), id TEXT NOT NULL, id_source TEXT, project_id TEXT, title TEXT, path TEXT, cwd TEXT, updated_at TIMESTAMPTZ, size_bytes BIGINT, content_sha256 TEXT, deleted_at TIMESTAMPTZ, indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, id))`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS id_source TEXT`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS cwd TEXT`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS history_chunks (agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, chunk_index INTEGER NOT NULL, sha256 TEXT NOT NULL, size_bytes BIGINT NOT NULL, data BYTEA, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, session_id, chunk_index, sha256))`,
		`ALTER TABLE history_chunks ADD COLUMN IF NOT EXISTS data BYTEA`,
		`CREATE TABLE IF NOT EXISTS history_messages (agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, message_index INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, message_at TIMESTAMPTZ, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, session_id, message_index))`,
		`CREATE TABLE IF NOT EXISTS history_items (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, item_index INTEGER NOT NULL, role TEXT, kind TEXT NOT NULL, text TEXT, payload JSONB, source TEXT, source_event_id TEXT, item_at TIMESTAMPTZ, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE(agent_id, session_id, item_index, kind))`,
		`CREATE TABLE IF NOT EXISTS sync_cursors (agent_id TEXT NOT NULL REFERENCES agents(id), stream TEXT NOT NULL, subject_id TEXT NOT NULL, cursor TEXT NOT NULL, cursor_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, stream, subject_id))`,
		`CREATE TABLE IF NOT EXISTS runs (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), command_id TEXT, project_id TEXT, session_id TEXT, status TEXT NOT NULL, status_reason TEXT, last_event_seq BIGINT NOT NULL DEFAULT 0, last_event_at TIMESTAMPTZ, started_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ)`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS command_id TEXT`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS status_reason TEXT`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS last_event_seq BIGINT NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN IF NOT EXISTS last_event_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS runtime_sessions (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), runtime TEXT NOT NULL, native_session_id TEXT, project_id TEXT, session_id TEXT, last_response_id TEXT, resume_mode TEXT, last_run_id TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS run_events (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), device_id TEXT NOT NULL, run_id TEXT, command_id TEXT, session_id TEXT, project_id TEXT, seq BIGINT NOT NULL DEFAULT 0, kind TEXT NOT NULL, payload JSONB, event_at TIMESTAMPTZ NOT NULL, received_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`ALTER TABLE run_events ADD COLUMN IF NOT EXISTS command_id TEXT`,
		`ALTER TABLE run_events ADD COLUMN IF NOT EXISTS seq BIGINT NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS run_event_gaps (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT NOT NULL, expected_seq BIGINT NOT NULL, observed_seq BIGINT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, detected_at TIMESTAMPTZ NOT NULL DEFAULT now(), resolved_at TIMESTAMPTZ)`,
		`CREATE TABLE IF NOT EXISTS run_steps (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT NOT NULL, seq_start BIGINT NOT NULL, seq_end BIGINT, kind TEXT NOT NULL, title TEXT, status TEXT NOT NULL, payload JSONB, started_at TIMESTAMPTZ NOT NULL, finished_at TIMESTAMPTZ, UNIQUE(agent_id, run_id, seq_start, kind))`,
		`CREATE TABLE IF NOT EXISTS run_messages (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT NOT NULL, seq_start BIGINT NOT NULL, seq_end BIGINT, role TEXT NOT NULL, content TEXT, payload JSONB, status TEXT NOT NULL, started_at TIMESTAMPTZ NOT NULL, finished_at TIMESTAMPTZ, UNIQUE(agent_id, run_id, seq_start, role))`,
		`CREATE TABLE IF NOT EXISTS tool_calls (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT NOT NULL, seq_start BIGINT NOT NULL, seq_end BIGINT, tool_call_id TEXT, tool_name TEXT, arguments JSONB, output JSONB, status TEXT NOT NULL, approval_request_id TEXT, started_at TIMESTAMPTZ NOT NULL, finished_at TIMESTAMPTZ, UNIQUE(agent_id, run_id, tool_call_id))`,
		`CREATE TABLE IF NOT EXISTS commands (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT, session_id TEXT, project_id TEXT, kind TEXT NOT NULL, mode TEXT, payload JSONB, status TEXT NOT NULL, status_reason TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), dispatched_at TIMESTAMPTZ, acked_at TIMESTAMPTZ, deadline_at TIMESTAMPTZ, expires_at TIMESTAMPTZ, retry_count INTEGER NOT NULL DEFAULT 0, idempotency_key TEXT)`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS mode TEXT`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS status_reason TEXT`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS dispatched_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS retry_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS idempotency_key TEXT`,
		`CREATE TABLE IF NOT EXISTS command_attempts (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), command_id TEXT NOT NULL, attempt_no INTEGER NOT NULL, phase TEXT NOT NULL, status TEXT NOT NULL, reason TEXT, payload JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
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
		`CREATE INDEX IF NOT EXISTS idx_sessions_project ON codex_sessions(agent_id, project_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_history_messages_session ON history_messages(agent_id, session_id, message_index)`,
		`CREATE INDEX IF NOT EXISTS idx_history_items_session ON history_items(agent_id, session_id, item_index)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_cursors_agent ON sync_cursors(agent_id, stream, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runtime_sessions_agent ON runtime_sessions(agent_id, updated_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_runtime_sessions_native ON runtime_sessions(agent_id, runtime, native_session_id) WHERE native_session_id IS NOT NULL AND native_session_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(agent_id, run_id, seq, received_at)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_agent ON run_events(agent_id, received_at)`,
		`CREATE INDEX IF NOT EXISTS idx_run_event_gaps_run ON run_event_gaps(agent_id, run_id, status, detected_at)`,
		`CREATE INDEX IF NOT EXISTS idx_run_steps_run ON run_steps(agent_id, run_id, seq_start)`,
		`CREATE INDEX IF NOT EXISTS idx_run_messages_run ON run_messages(agent_id, run_id, seq_start)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_run ON tool_calls(agent_id, run_id, seq_start)`,
		`CREATE INDEX IF NOT EXISTS idx_commands_agent_status ON commands(agent_id, status, created_at)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_commands_agent_idempotency ON commands(agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_command_attempts_command ON command_attempts(agent_id, command_id, created_at)`,
	}
	for _, stmt := range indexes {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}
