package localstate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/protocol"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("state database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) SaveCodexRoots(ctx context.Context, roots []codexscan.Root) error {
	if s == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := upsertRoots(ctx, tx, roots, false); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveScanResult(ctx context.Context, result codexscan.Result) (DeletedIndex, error) {
	if s == nil {
		return DeletedIndex{}, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeletedIndex{}, err
	}
	if err := upsertRoots(ctx, tx, result.Roots, true); err != nil {
		_ = tx.Rollback()
		return DeletedIndex{}, err
	}
	if err := upsertProjects(ctx, tx, result.Projects); err != nil {
		_ = tx.Rollback()
		return DeletedIndex{}, err
	}
	if err := upsertSessions(ctx, tx, result.Sessions); err != nil {
		_ = tx.Rollback()
		return DeletedIndex{}, err
	}
	if err := upsertRuntimeSessions(ctx, tx, result.RuntimeSessions); err != nil {
		_ = tx.Rollback()
		return DeletedIndex{}, err
	}
	deleted, err := markMissingDeleted(ctx, tx, result)
	if err != nil {
		_ = tx.Rollback()
		return DeletedIndex{}, err
	}
	if err := saveKV(ctx, tx, "last_scan_at", now()); err != nil {
		_ = tx.Rollback()
		return DeletedIndex{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeletedIndex{}, err
	}
	return deleted, nil
}

func (s *Store) SaveHistoryChunk(ctx context.Context, chunk codexscan.HistoryChunk) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO history_chunks(session_id, chunk_index, sha256, size, updated_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(session_id, chunk_index) DO UPDATE SET
			sha256 = excluded.sha256,
			size = excluded.size,
			acked_at = CASE WHEN history_chunks.sha256 = excluded.sha256 THEN history_chunks.acked_at ELSE NULL END,
			sent_at = CASE WHEN history_chunks.sha256 = excluded.sha256 THEN history_chunks.sent_at ELSE NULL END,
			updated_at = excluded.updated_at
	`, chunk.SessionID, chunk.Index, chunk.SHA256, len(chunk.Data), now())
	return err
}

func (s *Store) IsHistoryChunkAcked(ctx context.Context, sessionID string, index int, sha256 string) (bool, error) {
	if s == nil {
		return false, nil
	}
	var ackedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT acked_at FROM history_chunks
		WHERE session_id = ? AND chunk_index = ? AND sha256 = ?
	`, sessionID, index, sha256).Scan(&ackedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return ackedAt.Valid && ackedAt.String != "", nil
}

func (s *Store) MarkHistoryChunkSent(ctx context.Context, sessionID string, index int, sha256 string) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE history_chunks
		SET sent_at = ?, retry_count = retry_count + 1, last_error = NULL, updated_at = ?
		WHERE session_id = ? AND chunk_index = ? AND sha256 = ?
	`, now(), now(), sessionID, index, sha256)
	return err
}

func (s *Store) MarkHistoryChunkAcked(ctx context.Context, ack HistoryChunkAck) error {
	if s == nil {
		return nil
	}
	args := []any{now(), now(), ack.SessionID, ack.Index}
	where := "session_id = ? AND chunk_index = ?"
	if ack.SHA256 != "" {
		where += " AND sha256 = ?"
		args = append(args, ack.SHA256)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE history_chunks
		SET acked_at = ?, updated_at = ?
		WHERE `+where, args...)
	return err
}

func (s *Store) MarkHistoryChunkError(ctx context.Context, sessionID string, index int, sha256 string, chunkErr error) error {
	if s == nil || chunkErr == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE history_chunks
		SET last_error = ?, updated_at = ?
		WHERE session_id = ? AND chunk_index = ? AND sha256 = ?
	`, chunkErr.Error(), now(), sessionID, index, sha256)
	return err
}

func (s *Store) SaveSyncCursor(ctx context.Context, cursor SyncCursor) error {
	if s == nil {
		return nil
	}
	subjectID := cursor.SubjectID
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_cursors(stream, subject_id, cursor, updated_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(stream, subject_id) DO UPDATE SET
			cursor = excluded.cursor,
			updated_at = excluded.updated_at
	`, cursor.Stream, subjectID, cursor.Cursor, now())
	return err
}

func (s *Store) SyncCursor(ctx context.Context, stream, subjectID string) (SyncCursor, error) {
	if s == nil {
		return SyncCursor{}, sql.ErrNoRows
	}
	var cursor SyncCursor
	err := s.db.QueryRowContext(ctx, `
		SELECT stream, subject_id, cursor
		FROM sync_cursors
		WHERE stream = ? AND subject_id = ?
	`, stream, subjectID).Scan(&cursor.Stream, &cursor.SubjectID, &cursor.Cursor)
	return cursor, err
}

func (s *Store) SaveRuntimeSession(ctx context.Context, session RuntimeSession) error {
	if s == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runtime_sessions(session_id, runtime, native_session_id, last_response_id, resume_mode, last_run_id, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, runtime) DO UPDATE SET
			native_session_id = COALESCE(NULLIF(excluded.native_session_id, ''), runtime_sessions.native_session_id),
			last_response_id = COALESCE(NULLIF(excluded.last_response_id, ''), runtime_sessions.last_response_id),
			resume_mode = COALESCE(NULLIF(excluded.resume_mode, ''), runtime_sessions.resume_mode),
			last_run_id = COALESCE(NULLIF(excluded.last_run_id, ''), runtime_sessions.last_run_id),
			updated_at = excluded.updated_at
	`, session.SessionID, session.Runtime, session.NativeSessionID, session.LastResponseID, session.ResumeMode, session.LastRunID, now())
	return err
}

func (s *Store) RuntimeSession(ctx context.Context, sessionID, runtime string) (RuntimeSession, error) {
	if s == nil {
		return RuntimeSession{}, sql.ErrNoRows
	}
	var session RuntimeSession
	err := s.db.QueryRowContext(ctx, `
		SELECT session_id, runtime, native_session_id, last_response_id, resume_mode, last_run_id
		FROM runtime_sessions
		WHERE session_id = ? AND runtime = ?
	`, sessionID, runtime).Scan(&session.SessionID, &session.Runtime, &session.NativeSessionID, &session.LastResponseID, &session.ResumeMode, &session.LastRunID)
	return session, err
}

func (s *Store) SaveCommandAck(ctx context.Context, ack protocol.Ack) error {
	return s.saveCommandAckRecord(ctx, CommandAck{
		CommandID: ack.CommandID,
		RunID:     ack.RunID,
		Status:    ack.Status,
		Message:   ack.Message,
		AckSeq:    ack.AckSeq,
		At:        ack.At,
	})
}

func (s *Store) saveCommandAckRecord(ctx context.Context, ack CommandAck) error {
	if s == nil || strings.TrimSpace(ack.CommandID) == "" {
		return nil
	}
	if ack.At.IsZero() {
		ack.At = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO command_acks(command_id, run_id, status, message, ack_seq, ack_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(command_id) DO UPDATE SET
			run_id = excluded.run_id,
			status = excluded.status,
			message = excluded.message,
			ack_seq = excluded.ack_seq,
			ack_at = excluded.ack_at,
			updated_at = excluded.updated_at
	`, ack.CommandID, ack.RunID, ack.Status, ack.Message, ack.AckSeq, ack.At.UTC().Format(time.RFC3339Nano), now())
	return err
}

func (s *Store) CommandAck(ctx context.Context, commandID string) (protocol.Ack, bool, error) {
	ack, err := s.commandAckRecord(ctx, commandID)
	if errors.Is(err, sql.ErrNoRows) {
		return protocol.Ack{}, false, nil
	}
	if err != nil {
		return protocol.Ack{}, false, err
	}
	return protocol.Ack{
		CommandID: ack.CommandID,
		RunID:     ack.RunID,
		Status:    ack.Status,
		Message:   ack.Message,
		AckSeq:    ack.AckSeq,
		At:        ack.At,
	}, true, nil
}

func (s *Store) commandAckRecord(ctx context.Context, commandID string) (CommandAck, error) {
	if s == nil || strings.TrimSpace(commandID) == "" {
		return CommandAck{}, sql.ErrNoRows
	}
	var ack CommandAck
	var ackAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT command_id, run_id, status, message, ack_seq, ack_at
		FROM command_acks
		WHERE command_id = ?
	`, commandID).Scan(&ack.CommandID, &ack.RunID, &ack.Status, &ack.Message, &ack.AckSeq, &ackAt)
	if err != nil {
		return CommandAck{}, err
	}
	if parsed, parseErr := time.Parse(time.RFC3339Nano, ackAt); parseErr == nil {
		ack.At = parsed.UTC()
	}
	return ack, nil
}

type HistoryChunkAck struct {
	SessionID string
	Index     int
	SHA256    string
}

type SyncCursor struct {
	Stream    string
	SubjectID string
	Cursor    string
}

type RuntimeSession struct {
	SessionID       string
	Runtime         string
	NativeSessionID string
	LastResponseID  string
	ResumeMode      string
	LastRunID       string
}

type CommandAck struct {
	CommandID string
	RunID     string
	Status    string
	Message   string
	AckSeq    uint64
	At        time.Time
}

type DeletedIndex struct {
	Projects []DeletedRef
	Sessions []DeletedRef
}

type DeletedRef struct {
	ID        string
	DeletedAt time.Time
}

func (s *Store) migrate(ctx context.Context) error {
	stmts := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS kv (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS codex_roots (
			path TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			source TEXT NOT NULL,
			exists INTEGER NOT NULL,
			first_seen_at TEXT NOT NULL,
			last_seen_at TEXT NOT NULL,
			last_scanned_at TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			root_path TEXT,
			name TEXT NOT NULL,
			path TEXT NOT NULL,
			last_active_at TEXT,
			has_agents_md INTEGER NOT NULL,
			git_branch TEXT,
			fingerprint TEXT NOT NULL,
			synced_at TEXT,
			deleted_at TEXT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_projects_path ON projects(path)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			id_source TEXT,
			root_path TEXT,
			project_id TEXT,
			title TEXT,
			path TEXT NOT NULL,
			cwd TEXT,
			updated_at_remote TEXT,
			size INTEGER NOT NULL,
			content_sha256 TEXT,
			fingerprint TEXT NOT NULL,
			synced_at TEXT,
			deleted_at TEXT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_project ON sessions(project_id)`,
		`ALTER TABLE sessions ADD COLUMN id_source TEXT`,
		`ALTER TABLE sessions ADD COLUMN cwd TEXT`,
		`CREATE TABLE IF NOT EXISTS history_chunks (
			session_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			sha256 TEXT NOT NULL,
			size INTEGER NOT NULL,
			sent_at TEXT,
			acked_at TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(session_id, chunk_index)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_history_chunks_pending ON history_chunks(acked_at, sent_at)`,
		`CREATE TABLE IF NOT EXISTS sync_cursors (
			stream TEXT NOT NULL,
			subject_id TEXT NOT NULL,
			cursor TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(stream, subject_id)
		)`,
		`CREATE TABLE IF NOT EXISTS runtime_sessions (
			session_id TEXT NOT NULL,
			runtime TEXT NOT NULL,
			native_session_id TEXT,
			last_response_id TEXT,
			resume_mode TEXT,
			last_run_id TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(session_id, runtime)
		)`,
		`CREATE TABLE IF NOT EXISTS command_acks (
			command_id TEXT PRIMARY KEY,
			run_id TEXT,
			status TEXT NOT NULL,
			message TEXT,
			ack_seq INTEGER NOT NULL DEFAULT 0,
			ack_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_command_acks_run ON command_acks(run_id)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return err
		}
	}
	return nil
}

type txLike interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryTxLike interface {
	txLike
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func upsertRoots(ctx context.Context, tx txLike, roots []codexscan.Root, scanned bool) error {
	for _, root := range roots {
		source := root.Source
		if source == "" {
			source = "unknown"
		}
		lastScannedAt := sql.NullString{}
		if scanned {
			lastScannedAt = sql.NullString{String: now(), Valid: true}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO codex_roots(path, kind, source, exists, first_seen_at, last_seen_at, last_scanned_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET
				kind = excluded.kind,
				source = excluded.source,
				exists = excluded.exists,
				last_seen_at = excluded.last_seen_at,
				last_scanned_at = COALESCE(excluded.last_scanned_at, codex_roots.last_scanned_at)
		`, root.Path, root.Kind, source, boolInt(root.Exists), now(), now(), lastScannedAt); err != nil {
			return err
		}
	}
	return nil
}

func upsertProjects(ctx context.Context, tx txLike, projects []codexscan.Project) error {
	for _, project := range projects {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO projects(id, root_path, name, path, last_active_at, has_agents_md, git_branch, fingerprint, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				root_path = excluded.root_path,
				name = excluded.name,
				path = excluded.path,
				last_active_at = excluded.last_active_at,
				has_agents_md = excluded.has_agents_md,
				git_branch = excluded.git_branch,
				fingerprint = excluded.fingerprint,
				deleted_at = NULL,
				updated_at = excluded.updated_at
		`, project.ID, project.Root, project.Name, project.Path, timeString(project.LastActiveAt), boolInt(project.HasAgentsMD), project.GitBranch, fingerprint(project), now()); err != nil {
			return err
		}
	}
	return nil
}

func upsertSessions(ctx context.Context, tx txLike, sessions []codexscan.Session) error {
	for _, session := range sessions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO sessions(id, id_source, root_path, project_id, title, path, cwd, updated_at_remote, size, content_sha256, fingerprint, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				id_source = excluded.id_source,
				root_path = excluded.root_path,
				project_id = excluded.project_id,
				title = excluded.title,
				path = excluded.path,
				cwd = excluded.cwd,
				updated_at_remote = excluded.updated_at_remote,
				size = excluded.size,
				content_sha256 = excluded.content_sha256,
				fingerprint = excluded.fingerprint,
				deleted_at = NULL,
				updated_at = excluded.updated_at
		`, session.ID, session.IDSource, session.Root, session.ProjectID, session.Title, session.Path, session.CWD, timeString(session.UpdatedAt), session.Size, session.ContentSHA256, fingerprint(session), now()); err != nil {
			return err
		}
	}
	return nil
}

func upsertRuntimeSessions(ctx context.Context, tx txLike, sessions []codexscan.RuntimeSession) error {
	for _, session := range sessions {
		if session.SessionID == "" || session.Runtime == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO runtime_sessions(session_id, runtime, native_session_id, last_response_id, resume_mode, last_run_id, updated_at)
			VALUES(?, ?, ?, ?, ?, '', ?)
			ON CONFLICT(session_id, runtime) DO UPDATE SET
				native_session_id = COALESCE(NULLIF(excluded.native_session_id, ''), runtime_sessions.native_session_id),
				last_response_id = COALESCE(NULLIF(excluded.last_response_id, ''), runtime_sessions.last_response_id),
				resume_mode = COALESCE(NULLIF(excluded.resume_mode, ''), runtime_sessions.resume_mode),
				updated_at = excluded.updated_at
		`, session.SessionID, session.Runtime, session.NativeSessionID, session.LastResponseID, session.ResumeMode, now()); err != nil {
			return err
		}
	}
	return nil
}

func markMissingDeleted(ctx context.Context, tx queryTxLike, result codexscan.Result) (DeletedIndex, error) {
	deletedAt := time.Now().UTC()
	seenProjects := map[string]struct{}{}
	for _, project := range result.Projects {
		seenProjects[project.ID] = struct{}{}
	}
	seenSessions := map[string]struct{}{}
	for _, session := range result.Sessions {
		seenSessions[session.ID] = struct{}{}
	}
	scannedRoots := map[string]struct{}{}
	for _, root := range result.Roots {
		if root.Exists {
			scannedRoots[root.Path] = struct{}{}
		}
	}

	deletedProjects, err := missingIDs(ctx, tx, "projects", "root_path", seenProjects, scannedRoots)
	if err != nil {
		return DeletedIndex{}, err
	}
	deletedSessions, err := missingIDs(ctx, tx, "sessions", "root_path", seenSessions, scannedRoots)
	if err != nil {
		return DeletedIndex{}, err
	}
	for _, id := range deletedProjects {
		if _, err := tx.ExecContext(ctx, `UPDATE projects SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, deletedAt.Format(time.RFC3339Nano), now(), id); err != nil {
			return DeletedIndex{}, err
		}
	}
	for _, id := range deletedSessions {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, deletedAt.Format(time.RFC3339Nano), now(), id); err != nil {
			return DeletedIndex{}, err
		}
	}
	return DeletedIndex{
		Projects: deletedRefs(deletedProjects, deletedAt),
		Sessions: deletedRefs(deletedSessions, deletedAt),
	}, nil
}

func missingIDs(ctx context.Context, tx queryTxLike, table, rootColumn string, seen, scannedRoots map[string]struct{}) ([]string, error) {
	if len(scannedRoots) == 0 {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, `+rootColumn+` FROM `+table+` WHERE deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		var root sql.NullString
		if err := rows.Scan(&id, &root); err != nil {
			return nil, err
		}
		if root.Valid {
			if _, scanned := scannedRoots[root.String]; !scanned {
				continue
			}
		}
		if _, ok := seen[id]; !ok {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

func deletedRefs(ids []string, deletedAt time.Time) []DeletedRef {
	out := make([]DeletedRef, 0, len(ids))
	for _, id := range ids {
		out = append(out, DeletedRef{ID: id, DeletedAt: deletedAt})
	}
	return out
}

func saveKV(ctx context.Context, tx txLike, key, value string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO kv(key, value, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at
	`, key, value, now())
	return err
}

func fingerprint(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return strconv.FormatUint(fnv64(raw), 16)
}

func fnv64(data []byte) uint64 {
	var hash uint64 = 14695981039346656037
	for _, b := range data {
		hash ^= uint64(b)
		hash *= 1099511628211
	}
	return hash
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeString(value time.Time) sql.NullString {
	if value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: value.UTC().Format(time.RFC3339Nano), Valid: true}
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
