package store

import (
	"context"
	"errors"
)

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_reason TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled_by TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS user_keys (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), base_url TEXT NOT NULL, key_fingerprint TEXT NOT NULL, disabled_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS base_url_fingerprint TEXT`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS name TEXT`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS provider TEXT`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS model TEXT`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS disabled_reason TEXT`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS disabled_by TEXT`,
		`ALTER TABLE user_keys ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS devices (id TEXT PRIMARY KEY, user_id TEXT NOT NULL REFERENCES users(id), install_id TEXT NOT NULL, machine_hash TEXT NOT NULL, hostname TEXT, os TEXT, arch TEXT, username_hash TEXT, agent_version TEXT, install_state JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ)`,
		`ALTER TABLE devices ADD COLUMN IF NOT EXISTS install_state JSONB`,
		`ALTER TABLE devices ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ`,
		`ALTER TABLE devices ADD COLUMN IF NOT EXISTS disabled_reason TEXT`,
		`ALTER TABLE devices ADD COLUMN IF NOT EXISTS disabled_by TEXT`,
		`CREATE TABLE IF NOT EXISTS agents (id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, user_id TEXT NOT NULL REFERENCES users(id), device_id TEXT NOT NULL UNIQUE REFERENCES devices(id), agent_secret TEXT NOT NULL, server_key_id TEXT NOT NULL, protocol_version TEXT, capabilities JSONB, credential JSONB, registered_at TIMESTAMPTZ, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), last_seen_at TIMESTAMPTZ)`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS protocol_version TEXT`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS capabilities JSONB`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS credential JSONB`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS registered_at TIMESTAMPTZ`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS disabled_at TIMESTAMPTZ`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS disabled_reason TEXT`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS disabled_by TEXT`,
		`ALTER TABLE agents ADD COLUMN IF NOT EXISTS agent_epoch BIGINT NOT NULL DEFAULT 1`,
		`CREATE TABLE IF NOT EXISTS codex_roots (agent_id TEXT NOT NULL REFERENCES agents(id), path TEXT NOT NULL, kind TEXT, source TEXT, exists BOOLEAN NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, path))`,
		`CREATE TABLE IF NOT EXISTS projects (agent_id TEXT NOT NULL REFERENCES agents(id), id TEXT NOT NULL, name TEXT, path TEXT, last_active_at TIMESTAMPTZ, has_agents_md BOOLEAN NOT NULL DEFAULT false, git_branch TEXT, deleted_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, id))`,
		`ALTER TABLE projects ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS codex_sessions (agent_id TEXT NOT NULL REFERENCES agents(id), id TEXT NOT NULL, id_source TEXT, project_id TEXT, title TEXT, path TEXT, cwd TEXT, updated_at TIMESTAMPTZ, size_bytes BIGINT, content_sha256 TEXT, deleted_at TIMESTAMPTZ, indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, id))`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS id_source TEXT`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS cwd TEXT`,
		`ALTER TABLE codex_sessions ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS history_chunks (agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, chunk_index INTEGER NOT NULL, sha256 TEXT NOT NULL, size_bytes BIGINT NOT NULL, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, session_id, chunk_index, sha256))`,
		`ALTER TABLE history_chunks DROP COLUMN IF EXISTS data`,
		`CREATE TABLE IF NOT EXISTS history_messages (agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, message_index INTEGER NOT NULL, role TEXT NOT NULL, text TEXT NOT NULL, message_at TIMESTAMPTZ, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, session_id, message_index))`,
		`CREATE TABLE IF NOT EXISTS history_items (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), session_id TEXT NOT NULL, item_index INTEGER NOT NULL, role TEXT, kind TEXT NOT NULL, text TEXT, payload JSONB, source TEXT, source_event_id TEXT, item_at TIMESTAMPTZ, received_at TIMESTAMPTZ NOT NULL DEFAULT now(), UNIQUE(agent_id, session_id, item_index, kind))`,
		`CREATE TABLE IF NOT EXISTS sync_cursors (agent_id TEXT NOT NULL REFERENCES agents(id), stream TEXT NOT NULL, subject_id TEXT NOT NULL, cursor TEXT NOT NULL, cursor_at TIMESTAMPTZ, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(agent_id, stream, subject_id))`,
		`CREATE TABLE IF NOT EXISTS runs (id TEXT NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id), command_id TEXT, project_id TEXT, session_id TEXT, status TEXT NOT NULL, status_reason TEXT, last_event_seq BIGINT NOT NULL DEFAULT 0, last_event_at TIMESTAMPTZ, started_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ, PRIMARY KEY(agent_id, id))`,
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
		`CREATE TABLE IF NOT EXISTS commands (id TEXT NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT, session_id TEXT, project_id TEXT, kind TEXT NOT NULL, mode TEXT, payload JSONB, status TEXT NOT NULL, status_reason TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), dispatched_at TIMESTAMPTZ, acked_at TIMESTAMPTZ, deadline_at TIMESTAMPTZ, expires_at TIMESTAMPTZ, retry_count INTEGER NOT NULL DEFAULT 0, idempotency_key TEXT, PRIMARY KEY(agent_id, id))`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS mode TEXT`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS status_reason TEXT`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS dispatched_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS deadline_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS retry_count INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS idempotency_key TEXT`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS dispatch_claimed_by TEXT`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS dispatch_claimed_at TIMESTAMPTZ`,
		`ALTER TABLE commands ADD COLUMN IF NOT EXISTS dispatch_claimed_until TIMESTAMPTZ`,
		`CREATE TABLE IF NOT EXISTS command_attempts (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), command_id TEXT NOT NULL, attempt_no INTEGER NOT NULL, phase TEXT NOT NULL, status TEXT NOT NULL, reason TEXT, payload JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS heartbeats (agent_id TEXT PRIMARY KEY REFERENCES agents(id), device_id TEXT NOT NULL, status TEXT NOT NULL, payload JSONB NOT NULL, received_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE TABLE IF NOT EXISTS approval_requests (id TEXT PRIMARY KEY, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT, project_id TEXT, session_id TEXT, category TEXT, action TEXT, risk_level TEXT, payload JSONB, status TEXT NOT NULL, requested_at TIMESTAMPTZ NOT NULL DEFAULT now(), decided_at TIMESTAMPTZ)`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS decision TEXT`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS decision_reason TEXT`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS decision_command_id TEXT`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS decision_queued_at TIMESTAMPTZ`,
		`ALTER TABLE approval_requests ADD COLUMN IF NOT EXISTS decided_by TEXT`,
		`CREATE TABLE IF NOT EXISTS audit_logs (id TEXT PRIMARY KEY, user_id TEXT, agent_id TEXT, action TEXT NOT NULL, payload JSONB, created_at TIMESTAMPTZ NOT NULL DEFAULT now())`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureRunsAgentScopedPrimaryKey(ctx); err != nil {
		return err
	}
	if err := s.ensureCommandsAgentScopedPrimaryKey(ctx); err != nil {
		return err
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_agents_user ON agents(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_users_active ON users(created_at DESC) WHERE disabled_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_agents_active_user ON agents(user_id, last_seen_at DESC) WHERE disabled_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_user_keys_credential ON user_keys(user_id, key_fingerprint, base_url_fingerprint) WHERE disabled_at IS NULL`,
		`CREATE INDEX IF NOT EXISTS idx_user_keys_user ON user_keys(user_id, created_at DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_agents_device ON agents(device_id)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_user ON devices(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_devices_active_user ON devices(user_id, last_seen_at DESC) WHERE disabled_at IS NULL`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_install ON devices(user_id, install_id, machine_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_agent ON projects(agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_agent ON codex_sessions(agent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_project ON codex_sessions(agent_id, project_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_history_messages_session ON history_messages(agent_id, session_id, message_index)`,
		`CREATE INDEX IF NOT EXISTS idx_history_items_session ON history_items(agent_id, session_id, item_index)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_cursors_agent ON sync_cursors(agent_id, stream, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runtime_sessions_agent ON runtime_sessions(agent_id, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_runtime_sessions_session ON runtime_sessions(agent_id, session_id) WHERE session_id IS NOT NULL AND session_id <> ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_runtime_sessions_native ON runtime_sessions(agent_id, runtime, native_session_id) WHERE native_session_id IS NOT NULL AND native_session_id <> ''`,
		`DELETE FROM run_events a USING run_events b WHERE a.agent_id=b.agent_id AND a.run_id=b.run_id AND a.seq=b.seq AND a.run_id IS NOT NULL AND a.run_id <> '' AND a.seq > 0 AND (a.received_at, a.id) > (b.received_at, b.id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_run_events_unique_seq ON run_events(agent_id, run_id, seq) WHERE run_id IS NOT NULL AND run_id <> '' AND seq > 0`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(agent_id, run_id, seq, received_at)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_agent ON run_events(agent_id, received_at)`,
		`CREATE INDEX IF NOT EXISTS idx_run_event_gaps_run ON run_event_gaps(agent_id, run_id, status, detected_at)`,
		`UPDATE runs older SET status='stale', status_reason='superseded_by_newer_active_session_run' FROM runs newer WHERE older.agent_id=newer.agent_id AND older.session_id=newer.session_id AND older.id<>newer.id AND older.session_id IS NOT NULL AND older.session_id<>'' AND older.status IN ('queued','accepted','running','awaiting_approval') AND newer.status IN ('queued','accepted','running','awaiting_approval') AND (older.started_at, older.id) < (newer.started_at, newer.id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_runs_active_session ON runs(agent_id, session_id) WHERE session_id IS NOT NULL AND session_id <> '' AND status IN ('queued','accepted','running','awaiting_approval')`,
		`CREATE INDEX IF NOT EXISTS idx_run_steps_run ON run_steps(agent_id, run_id, seq_start)`,
		`CREATE INDEX IF NOT EXISTS idx_run_messages_run ON run_messages(agent_id, run_id, seq_start)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_run ON tool_calls(agent_id, run_id, seq_start)`,
		`CREATE INDEX IF NOT EXISTS idx_commands_agent_status ON commands(agent_id, status, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_commands_dispatch_claim ON commands(status, dispatch_claimed_until, created_at) WHERE status IN ('queued','dispatch_failed','dispatched')`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_commands_agent_idempotency ON commands(agent_id, idempotency_key) WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_command_attempts_command ON command_attempts(agent_id, command_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_user_action ON audit_logs(user_id, action, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_logs_agent ON audit_logs(agent_id, created_at DESC)`,
	}
	for _, stmt := range indexes {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureRunsAgentScopedPrimaryKey(ctx context.Context) error {
	if ok, err := s.runsPrimaryKeyIsAgentScoped(ctx); err != nil || ok {
		return err
	}
	var duplicateIDs int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM (SELECT id FROM runs GROUP BY id HAVING COUNT(DISTINCT agent_id) > 1) duplicated`).Scan(&duplicateIDs); err != nil {
		return err
	}
	if duplicateIDs > 0 {
		return errors.New("cannot migrate runs primary key: duplicate run ids exist across agents")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `ALTER TABLE runs RENAME TO runs_legacy_id_pk`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE runs (id TEXT NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id), command_id TEXT, project_id TEXT, session_id TEXT, status TEXT NOT NULL, status_reason TEXT, last_event_seq BIGINT NOT NULL DEFAULT 0, last_event_at TIMESTAMPTZ, started_at TIMESTAMPTZ NOT NULL DEFAULT now(), finished_at TIMESTAMPTZ, PRIMARY KEY(agent_id, id))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runs (id, agent_id, command_id, project_id, session_id, status, status_reason, last_event_seq, last_event_at, started_at, finished_at)
		SELECT id, agent_id, command_id, project_id, session_id, status, status_reason, last_event_seq, last_event_at, started_at, finished_at FROM runs_legacy_id_pk`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DROP TABLE runs_legacy_id_pk`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) runsPrimaryKeyIsAgentScoped(ctx context.Context) (bool, error) {
	return s.primaryKeyIs(ctx, "runs", "agent_id", "id")
}

func (s *Store) ensureCommandsAgentScopedPrimaryKey(ctx context.Context) error {
	if ok, err := s.commandsPrimaryKeyIsAgentScoped(ctx); err != nil || ok {
		return err
	}
	var duplicatePairs int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM (SELECT agent_id, id FROM commands GROUP BY agent_id, id HAVING COUNT(*) > 1) duplicated`).Scan(&duplicatePairs); err != nil {
		return err
	}
	if duplicatePairs > 0 {
		return errors.New("cannot migrate commands primary key: duplicate command ids exist within an agent")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `ALTER TABLE commands RENAME TO commands_legacy_id_pk`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE commands (id TEXT NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id), run_id TEXT, session_id TEXT, project_id TEXT, kind TEXT NOT NULL, mode TEXT, payload JSONB, status TEXT NOT NULL, status_reason TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), dispatched_at TIMESTAMPTZ, acked_at TIMESTAMPTZ, deadline_at TIMESTAMPTZ, expires_at TIMESTAMPTZ, retry_count INTEGER NOT NULL DEFAULT 0, idempotency_key TEXT, dispatch_claimed_by TEXT, dispatch_claimed_at TIMESTAMPTZ, dispatch_claimed_until TIMESTAMPTZ, PRIMARY KEY(agent_id, id))`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO commands (id, agent_id, run_id, session_id, project_id, kind, mode, payload, status, status_reason, created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, idempotency_key, dispatch_claimed_by, dispatch_claimed_at, dispatch_claimed_until)
		SELECT id, agent_id, run_id, session_id, project_id, kind, mode, payload, status, status_reason, created_at, dispatched_at, acked_at, deadline_at, expires_at, retry_count, idempotency_key, dispatch_claimed_by, dispatch_claimed_at, dispatch_claimed_until FROM commands_legacy_id_pk`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DROP TABLE commands_legacy_id_pk`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) commandsPrimaryKeyIsAgentScoped(ctx context.Context) (bool, error) {
	return s.primaryKeyIs(ctx, "commands", "agent_id", "id")
}

func (s *Store) primaryKeyIs(ctx context.Context, table string, expected ...string) (bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN LATERAL unnest(i.indkey) WITH ORDINALITY AS k(attnum, ord) ON true
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
		WHERE c.relname = $1 AND i.indisprimary
		ORDER BY k.ord
	`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	columns := []string{}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return false, err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(columns) != len(expected) {
		return false, nil
	}
	for i := range expected {
		if columns[i] != expected[i] {
			return false, nil
		}
	}
	return true, nil
}
