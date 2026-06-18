package localstate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

type ProjectRecord struct {
	ID          string
	RootPath    string
	Name        string
	Path        string
	HasAgentsMD bool
	GitBranch   string
}

type SessionRecord struct {
	ID        string
	IDSource  string
	RootPath  string
	ProjectID string
	Title     string
	Path      string
	CWD       string
}

type CommandContext struct {
	Project ProjectRecord
	Session SessionRecord
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

func (s *Store) CodexRoot(ctx context.Context) (string, error) {
	if s == nil {
		return "", sql.ErrNoRows
	}
	var path string
	err := s.db.QueryRowContext(ctx, `
		SELECT path FROM codex_roots
		WHERE is_present = 1
		ORDER BY CASE WHEN kind = 'codex_home' THEN 0 ELSE 1 END, last_seen_at DESC
		LIMIT 1
	`).Scan(&path)
	return path, err
}

func (s *Store) SaveSessionRecord(ctx context.Context, session codexscan.Session) error {
	if s == nil {
		return nil
	}
	return upsertSessions(ctx, s.db, []codexscan.Session{session})
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

func (s *Store) Project(ctx context.Context, projectID string) (ProjectRecord, error) {
	if s == nil {
		return ProjectRecord{}, sql.ErrNoRows
	}
	var project ProjectRecord
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(root_path, ''), name, path, has_agents_md, COALESCE(git_branch, '')
		FROM projects
		WHERE id = ? AND deleted_at IS NULL
	`, projectID).Scan(&project.ID, &project.RootPath, &project.Name, &project.Path, &project.HasAgentsMD, &project.GitBranch)
	return project, err
}

func (s *Store) Session(ctx context.Context, sessionID string) (SessionRecord, error) {
	if s == nil {
		return SessionRecord{}, sql.ErrNoRows
	}
	var session SessionRecord
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(id_source, ''), COALESCE(root_path, ''), COALESCE(project_id, ''), COALESCE(title, ''), path, COALESCE(cwd, '')
		FROM sessions
		WHERE id = ? AND deleted_at IS NULL
	`, sessionID).Scan(&session.ID, &session.IDSource, &session.RootPath, &session.ProjectID, &session.Title, &session.Path, &session.CWD)
	return session, err
}

func (s *Store) SessionPath(ctx context.Context, sessionID string) (string, error) {
	session, err := s.Session(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(session.Path) == "" {
		return "", sql.ErrNoRows
	}
	return session.Path, nil
}

func (s *Store) ProjectRoots(ctx context.Context) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT path FROM projects WHERE deleted_at IS NULL ORDER BY updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roots := []string{}
	seen := map[string]struct{}{}
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, err
		}
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		roots = append(roots, path)
	}
	return roots, rows.Err()
}

func (s *Store) ResolveCommandContext(ctx context.Context, command protocol.Command) (CommandContext, error) {
	var resolved CommandContext
	if strings.TrimSpace(command.SessionID) != "" {
		session, err := s.Session(ctx, command.SessionID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				runtimeSession, runtimeErr := s.RuntimeSession(ctx, command.SessionID, "openai.responses")
				if runtimeErr != nil {
					if errors.Is(runtimeErr, sql.ErrNoRows) {
						return resolved, errors.New("codex session is not indexed locally")
					}
					return resolved, runtimeErr
				}
				resolved.Session = SessionRecord{
					ID:        runtimeSession.SessionID,
					IDSource:  "runtime_session",
					ProjectID: runtimeSession.ProjectID,
					Title:     firstNonEmpty(runtimeSession.NativeSessionID, runtimeSession.LastResponseID, runtimeSession.SessionID),
				}
				if strings.TrimSpace(command.ProjectID) != "" && strings.TrimSpace(runtimeSession.ProjectID) != "" && command.ProjectID != runtimeSession.ProjectID {
					return resolved, errors.New("command project does not match runtime session project")
				}
				if strings.TrimSpace(command.ProjectID) == "" {
					command.ProjectID = runtimeSession.ProjectID
				}
			} else {
				return resolved, err
			}
		} else {
			resolved.Session = session
			if strings.TrimSpace(command.ProjectID) != "" && strings.TrimSpace(session.ProjectID) != "" && command.ProjectID != session.ProjectID {
				return resolved, errors.New("command project does not match indexed session project")
			}
			if strings.TrimSpace(command.ProjectID) == "" {
				command.ProjectID = session.ProjectID
			}
		}
	}
	if strings.TrimSpace(command.ProjectID) != "" {
		project, err := s.Project(ctx, command.ProjectID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return resolved, errors.New("codex project is not indexed locally")
			}
			return resolved, err
		}
		resolved.Project = project
	}
	if resolved.Project.ID == "" && strings.TrimSpace(resolved.Session.CWD) != "" {
		resolved.Project = ProjectRecord{
			ID:   stableLocalProjectID(resolved.Session.CWD),
			Name: filepath.Base(resolved.Session.CWD),
			Path: resolved.Session.CWD,
		}
	}
	return resolved, nil
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
	if strings.TrimSpace(session.Runtime) == "" {
		return errors.New("runtime session runtime is required")
	}
	if strings.TrimSpace(session.SessionID) == "" {
		session.SessionID = firstNonEmpty(session.NativeSessionID, session.LastResponseID, session.LastRunID)
	}
	if strings.TrimSpace(session.SessionID) == "" {
		return errors.New("runtime session identity is required")
	}
	updatedAt := session.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runtime_sessions(session_id, runtime, project_id, native_session_id, last_response_id, resume_mode, last_run_id, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, runtime) DO UPDATE SET
			project_id = COALESCE(NULLIF(excluded.project_id, ''), runtime_sessions.project_id),
			native_session_id = COALESCE(NULLIF(excluded.native_session_id, ''), runtime_sessions.native_session_id),
			last_response_id = COALESCE(NULLIF(excluded.last_response_id, ''), runtime_sessions.last_response_id),
			resume_mode = COALESCE(NULLIF(excluded.resume_mode, ''), runtime_sessions.resume_mode),
			last_run_id = COALESCE(NULLIF(excluded.last_run_id, ''), runtime_sessions.last_run_id),
			updated_at = excluded.updated_at
	`, session.SessionID, session.Runtime, session.ProjectID, session.NativeSessionID, session.LastResponseID, session.ResumeMode, session.LastRunID, updatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) RuntimeSession(ctx context.Context, sessionID, runtime string) (RuntimeSession, error) {
	if s == nil {
		return RuntimeSession{}, sql.ErrNoRows
	}
	sessionID = strings.TrimSpace(sessionID)
	runtime = strings.TrimSpace(runtime)
	if sessionID == "" || runtime == "" {
		return RuntimeSession{}, sql.ErrNoRows
	}
	var session RuntimeSession
	var updatedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT session_id, runtime, COALESCE(project_id, ''), COALESCE(native_session_id, ''), COALESCE(last_response_id, ''), COALESCE(resume_mode, ''), COALESCE(last_run_id, ''), updated_at
		FROM runtime_sessions
		WHERE runtime = ?
			AND (session_id = ? OR native_session_id = ? OR last_response_id = ?)
		ORDER BY updated_at DESC
		LIMIT 1
	`, runtime, sessionID, sessionID, sessionID).Scan(&session.SessionID, &session.Runtime, &session.ProjectID, &session.NativeSessionID, &session.LastResponseID, &session.ResumeMode, &session.LastRunID, &updatedAt)
	if parsed, parseErr := time.Parse(time.RFC3339Nano, updatedAt); parseErr == nil {
		session.UpdatedAt = parsed.UTC()
	}
	return session, err
}

func (s *Store) RuntimeSessions(ctx context.Context) ([]RuntimeSession, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT session_id, runtime, COALESCE(project_id, ''), COALESCE(native_session_id, ''), COALESCE(last_response_id, ''), COALESCE(resume_mode, ''), COALESCE(last_run_id, ''), updated_at
		FROM runtime_sessions
		ORDER BY updated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sessions := []RuntimeSession{}
	for rows.Next() {
		var session RuntimeSession
		var updatedAt string
		if err := rows.Scan(&session.SessionID, &session.Runtime, &session.ProjectID, &session.NativeSessionID, &session.LastResponseID, &session.ResumeMode, &session.LastRunID, &updatedAt); err != nil {
			return nil, err
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, updatedAt); parseErr == nil {
			session.UpdatedAt = parsed.UTC()
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
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

func (s *Store) SaveRunEvent(ctx context.Context, event protocol.RunEvent) error {
	if s == nil || strings.TrimSpace(event.EventID) == "" {
		return nil
	}
	if protocol.IsUsageKind(event.Kind) {
		return nil
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO run_events(event_id, run_id, command_id, project_id, session_id, seq, kind, payload, event_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.EventID, event.RunID, event.CommandID, event.ProjectID, event.SessionID, event.Seq, event.Kind, jsonRaw(event.Payload), event.At.UTC().Format(time.RFC3339Nano), now())
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		var existingEventID string
		err = s.db.QueryRowContext(ctx, `SELECT event_id FROM run_events WHERE event_id = ?`, event.EventID).Scan(&existingEventID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE run_events
		SET run_id = ?, command_id = ?, project_id = ?, session_id = ?, seq = ?, kind = ?, payload = ?, event_at = ?, updated_at = ?
		WHERE event_id = ?
	`, event.RunID, event.CommandID, event.ProjectID, event.SessionID, event.Seq, event.Kind, jsonRaw(event.Payload), event.At.UTC().Format(time.RFC3339Nano), now(), event.EventID)
	if err != nil {
		return err
	}
	if err := s.projectRunState(ctx, event); err != nil {
		return err
	}
	return s.projectRunView(ctx, event)
}

func (s *Store) MarkCodexHistorySynced(ctx context.Context, eventID, sessionID string) error {
	if s == nil || strings.TrimSpace(eventID) == "" || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO codex_history_sync(event_id, session_id, synced_at)
		VALUES(?, ?, ?)
	`, eventID, sessionID, now())
	return err
}

func (s *Store) CodexHistorySynced(ctx context.Context, eventID string) (bool, error) {
	if s == nil || strings.TrimSpace(eventID) == "" {
		return false, nil
	}
	var syncedAt string
	err := s.db.QueryRowContext(ctx, `SELECT synced_at FROM codex_history_sync WHERE event_id = ?`, eventID).Scan(&syncedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(syncedAt) != "", nil
}

func (s *Store) PendingRunEvents(ctx context.Context, limit int) ([]protocol.RunEvent, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT event_id, run_id, command_id, project_id, session_id, seq, kind, payload, event_at
		FROM run_events
		WHERE acked_at IS NULL
		ORDER BY event_at ASC, seq ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []protocol.RunEvent{}
	for rows.Next() {
		var event protocol.RunEvent
		var payload []byte
		var eventAt string
		if err := rows.Scan(&event.EventID, &event.RunID, &event.CommandID, &event.ProjectID, &event.SessionID, &event.Seq, &event.Kind, &payload, &eventAt); err != nil {
			return nil, err
		}
		if len(payload) > 0 {
			event.Payload = append(json.RawMessage(nil), payload...)
		}
		if parsed, parseErr := time.Parse(time.RFC3339Nano, eventAt); parseErr == nil {
			event.At = parsed.UTC()
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) MarkRunEventSent(ctx context.Context, event protocol.RunEvent) error {
	if s == nil || strings.TrimSpace(event.EventID) == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE run_events
		SET sent_at = ?, retry_count = retry_count + 1, last_error = NULL, updated_at = ?
		WHERE event_id = ?
	`, now(), now(), event.EventID)
	return err
}

func (s *Store) MarkRunEventError(ctx context.Context, event protocol.RunEvent, eventErr error) error {
	if s == nil || strings.TrimSpace(event.EventID) == "" || eventErr == nil {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE run_events
		SET last_error = ?, updated_at = ?
		WHERE event_id = ?
	`, eventErr.Error(), now(), event.EventID)
	return err
}

func (s *Store) MarkRunEventAcked(ctx context.Context, ack protocol.Ack) error {
	if s == nil {
		return nil
	}
	ackedAt := ack.At
	if ackedAt.IsZero() {
		ackedAt = time.Now().UTC()
	}
	if strings.TrimSpace(ack.EventID) != "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE run_events
			SET acked_at = ?, updated_at = ?
			WHERE event_id = ? AND acked_at IS NULL
		`, ackedAt.UTC().Format(time.RFC3339Nano), now(), ack.EventID)
		return err
	}
	if strings.TrimSpace(ack.RunID) == "" || ack.AckSeq == 0 {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE run_events
		SET acked_at = ?, updated_at = ?
		WHERE run_id = ? AND seq = ? AND acked_at IS NULL
	`, ackedAt.UTC().Format(time.RFC3339Nano), now(), ack.RunID, ack.AckSeq)
	return err
}

func (s *Store) LastRunEventSeq(ctx context.Context) (uint64, error) {
	if s == nil {
		return 0, nil
	}
	var seq uint64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM run_events`).Scan(&seq)
	return seq, err
}

func (s *Store) ReconcileInterruptedRuns(ctx context.Context) ([]protocol.RunEvent, error) {
	if s == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, COALESCE(command_id, ''), COALESCE(project_id, ''), COALESCE(session_id, '')
		FROM runs
		WHERE status IN ('queued','starting','running','awaiting_approval','stopping')
		ORDER BY updated_at ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type interruptedRun struct {
		RunID     string
		CommandID string
		ProjectID string
		SessionID string
	}
	runs := []interruptedRun{}
	for rows.Next() {
		var run interruptedRun
		if err := rows.Scan(&run.RunID, &run.CommandID, &run.ProjectID, &run.SessionID); err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	seq, err := s.LastRunEventSeq(ctx)
	if err != nil {
		return nil, err
	}
	events := make([]protocol.RunEvent, 0, len(runs))
	for _, run := range runs {
		seq++
		event := protocol.RunEvent{
			EventID:   protocol.NewID("evt"),
			RunID:     run.RunID,
			CommandID: run.CommandID,
			ProjectID: run.ProjectID,
			SessionID: run.SessionID,
			Seq:       seq,
			Kind:      "run.error",
			Payload:   protocol.Raw(map[string]string{"error": "agent restarted before run completed", "status": "interrupted"}),
			At:        time.Now().UTC(),
		}
		if err := s.SaveRunEvent(ctx, event); err != nil {
			return events, err
		}
		events = append(events, event)
	}
	return events, nil
}

func (s *Store) projectRunState(ctx context.Context, event protocol.RunEvent) error {
	if strings.TrimSpace(event.RunID) == "" {
		return nil
	}
	status, finished := runStatusFromEvent(event)
	if status == "" {
		return nil
	}
	statusReason := runEventReason(event)
	startedAt := event.At
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO runs(id, command_id, project_id, session_id, status, status_reason, last_event_seq, last_event_at, started_at, finished_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			command_id = COALESCE(NULLIF(excluded.command_id, ''), runs.command_id),
			project_id = COALESCE(NULLIF(excluded.project_id, ''), runs.project_id),
			session_id = COALESCE(NULLIF(excluded.session_id, ''), runs.session_id),
			status = CASE
				WHEN runs.status IN ('done','error','interrupted','stopped') AND excluded.status NOT IN ('done','error','interrupted','stopped') THEN runs.status
				ELSE excluded.status
			END,
			status_reason = COALESCE(NULLIF(excluded.status_reason, ''), runs.status_reason),
			last_event_seq = MAX(runs.last_event_seq, excluded.last_event_seq),
			last_event_at = excluded.last_event_at,
			started_at = COALESCE(runs.started_at, excluded.started_at),
			finished_at = COALESCE(excluded.finished_at, runs.finished_at),
			updated_at = excluded.updated_at
	`, event.RunID, event.CommandID, event.ProjectID, event.SessionID, status, statusReason, event.Seq, timeString(startedAt), timeString(startedAt), nullableTimeString(event.At, finished), now()); err != nil {
		return err
	}
	return nil
}

func (s *Store) projectRunView(ctx context.Context, event protocol.RunEvent) error {
	if strings.TrimSpace(event.RunID) == "" {
		return nil
	}
	switch event.Kind {
	case "assistant.message.delta":
		return s.appendRunMessage(ctx, event, "assistant", "streaming", false)
	case "assistant.message.done":
		return s.finishRunMessage(ctx, event, "assistant")
	case "user.message":
		return s.upsertRunMessage(ctx, event, "user", "done", true)
	case "tool.call.started", "tool.call.delta", "tool.call.done", "tool.output", "approval.requested":
		return s.upsertToolCall(ctx, event)
	default:
		return nil
	}
}

func (s *Store) appendRunMessage(ctx context.Context, event protocol.RunEvent, role, status string, finished bool) error {
	content := payloadText(event.Payload)
	if content == "" {
		return nil
	}
	id, existing, ok, err := s.lastRunMessage(ctx, event.RunID, role)
	if err != nil {
		return err
	}
	if ok {
		_, err := s.db.ExecContext(ctx, `
			UPDATE run_messages
			SET seq_end = ?, content = ?, payload = ?, status = ?, updated_at = ?
			WHERE id = ?
		`, event.Seq, existing+content, jsonRaw(event.Payload), status, now(), id)
		return err
	}
	return s.upsertRunMessage(ctx, event, role, status, finished)
}

func (s *Store) finishRunMessage(ctx context.Context, event protocol.RunEvent, role string) error {
	content := payloadText(event.Payload)
	id, _, ok, err := s.lastRunMessage(ctx, event.RunID, role)
	if err != nil {
		return err
	}
	if ok {
		_, err := s.db.ExecContext(ctx, `
			UPDATE run_messages
			SET seq_end = ?, content = COALESCE(NULLIF(?, ''), content), payload = ?, status = 'done', finished_at = ?, updated_at = ?
			WHERE id = ?
		`, event.Seq, content, jsonRaw(event.Payload), nullableTimeString(event.At, true), now(), id)
		return err
	}
	return s.upsertRunMessage(ctx, event, role, "done", true)
}

func (s *Store) lastRunMessage(ctx context.Context, runID, role string) (string, string, bool, error) {
	var id string
	var content string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, COALESCE(content, '')
		FROM run_messages
		WHERE run_id = ? AND role = ?
		ORDER BY seq_start DESC
		LIMIT 1
	`, runID, role).Scan(&id, &content)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	return id, content, true, nil
}

func (s *Store) upsertRunMessage(ctx context.Context, event protocol.RunEvent, role, status string, finished bool) error {
	content := payloadText(event.Payload)
	id := projectionID(event.RunID, strconv.FormatUint(event.Seq, 10), "message", role)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO run_messages(id, run_id, seq_start, seq_end, role, content, payload, status, started_at, finished_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, seq_start, role) DO UPDATE SET
			seq_end = excluded.seq_end,
			content = COALESCE(NULLIF(excluded.content, ''), run_messages.content),
			payload = excluded.payload,
			status = excluded.status,
			finished_at = excluded.finished_at,
			updated_at = excluded.updated_at
	`, id, event.RunID, event.Seq, event.Seq, role, content, jsonRaw(event.Payload), status, timeString(event.At), nullableTimeString(event.At, finished), now())
	return err
}

func (s *Store) upsertToolCall(ctx context.Context, event protocol.RunEvent) error {
	toolCallID := payloadString(event.Payload, "tool_call_id", "call_id", "id")
	if toolCallID == "" {
		toolCallID = projectionID(event.RunID, strconv.FormatUint(event.Seq, 10), "tool", event.Kind)
	}
	toolName := payloadString(event.Payload, "tool_name", "name", "tool")
	status := toolStatus(event.Kind)
	arguments := payloadObject(event.Payload, "arguments", "args")
	output := payloadObject(event.Payload, "output", "result")
	approvalID := payloadString(event.Payload, "approval_id")
	id := projectionID(event.RunID, toolCallID, "tool_call")
	finished := status == "done" || status == "output"
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO tool_calls(id, run_id, seq_start, seq_end, tool_call_id, tool_name, arguments, output, status, approval_request_id, started_at, finished_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, tool_call_id) DO UPDATE SET
			seq_end = excluded.seq_end,
			tool_name = COALESCE(NULLIF(excluded.tool_name, ''), tool_calls.tool_name),
			arguments = COALESCE(excluded.arguments, tool_calls.arguments),
			output = COALESCE(excluded.output, tool_calls.output),
			status = excluded.status,
			approval_request_id = COALESCE(NULLIF(excluded.approval_request_id, ''), tool_calls.approval_request_id),
			finished_at = COALESCE(excluded.finished_at, tool_calls.finished_at),
			updated_at = excluded.updated_at
	`, id, event.RunID, event.Seq, event.Seq, toolCallID, toolName, jsonRaw(arguments), jsonRaw(output), status, approvalID, timeString(event.At), nullableTimeString(event.At, finished), now())
	return err
}

func payloadText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range []string{"text", "delta", "content", "message"} {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func payloadString(raw json.RawMessage, keys ...string) string {
	if len(raw) == 0 {
		return ""
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	for _, key := range keys {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func payloadObject(raw json.RawMessage, keys ...string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	for _, key := range keys {
		if item, ok := value[key]; ok {
			return protocol.Raw(item)
		}
	}
	return nil
}

func toolStatus(kind string) string {
	switch kind {
	case "tool.call.started", "tool.call.delta":
		return "running"
	case "tool.call.done":
		return "done"
	case "tool.output":
		return "output"
	case "approval.requested":
		return "awaiting_approval"
	default:
		return "running"
	}
}

func runStatusFromEvent(event protocol.RunEvent) (string, bool) {
	switch event.Kind {
	case "run.started":
		return "running", false
	case "run.awaiting_approval":
		return "awaiting_approval", false
	case "run.done", "run.completed":
		return "done", true
	case "run.error", "run.failed":
		if strings.EqualFold(payloadString(event.Payload, "status"), "interrupted") || strings.Contains(runEventReason(event), "interrupted") {
			return "interrupted", true
		}
		return "error", true
	case "run.stopped":
		return "stopped", true
	default:
		return "", false
	}
}

func runEventReason(event protocol.RunEvent) string {
	if len(event.Payload) == 0 {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(event.Payload, &payload) != nil {
		return ""
	}
	for _, key := range []string{"error", "reason", "message", "status"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func jsonRaw(value any) []byte {
	if raw, ok := value.([]byte); ok {
		return raw
	}
	raw, _ := json.Marshal(value)
	return raw
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
	ProjectID       string
	NativeSessionID string
	LastResponseID  string
	ResumeMode      string
	LastRunID       string
	UpdatedAt       time.Time
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
			is_present INTEGER NOT NULL,
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
			project_id TEXT,
			native_session_id TEXT,
			last_response_id TEXT,
			resume_mode TEXT,
			last_run_id TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(session_id, runtime)
		)`,
		`ALTER TABLE runtime_sessions ADD COLUMN project_id TEXT`,
		`CREATE INDEX IF NOT EXISTS idx_runtime_sessions_native ON runtime_sessions(runtime, native_session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_runtime_sessions_response ON runtime_sessions(runtime, last_response_id)`,
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
		`CREATE TABLE IF NOT EXISTS run_events (
			event_id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			command_id TEXT,
			project_id TEXT,
			session_id TEXT,
			seq INTEGER NOT NULL,
			kind TEXT NOT NULL,
			payload BLOB,
			event_at TEXT NOT NULL,
			sent_at TEXT,
			acked_at TEXT,
			retry_count INTEGER NOT NULL DEFAULT 0,
			last_error TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE(run_id, seq)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_pending ON run_events(acked_at, event_at, seq)`,
		`CREATE INDEX IF NOT EXISTS idx_run_events_run_seq ON run_events(run_id, seq)`,
		`CREATE TABLE IF NOT EXISTS codex_history_sync (
			event_id TEXT PRIMARY KEY,
			session_id TEXT NOT NULL,
			synced_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			command_id TEXT,
			project_id TEXT,
			session_id TEXT,
			status TEXT NOT NULL,
			status_reason TEXT,
			last_event_seq INTEGER NOT NULL DEFAULT 0,
			last_event_at TEXT,
			started_at TEXT,
			finished_at TEXT,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status, updated_at)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_session ON runs(session_id, updated_at)`,
		`CREATE TABLE IF NOT EXISTS run_messages (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			seq_start INTEGER NOT NULL,
			seq_end INTEGER,
			role TEXT NOT NULL,
			content TEXT,
			payload BLOB,
			status TEXT NOT NULL,
			started_at TEXT,
			finished_at TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE(run_id, seq_start, role)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_run_messages_run ON run_messages(run_id, seq_start)`,
		`CREATE TABLE IF NOT EXISTS tool_calls (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL,
			seq_start INTEGER NOT NULL,
			seq_end INTEGER,
			tool_call_id TEXT NOT NULL,
			tool_name TEXT,
			arguments BLOB,
			output BLOB,
			status TEXT NOT NULL,
			approval_request_id TEXT,
			started_at TEXT,
			finished_at TEXT,
			updated_at TEXT NOT NULL,
			UNIQUE(run_id, tool_call_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tool_calls_run ON tool_calls(run_id, seq_start)`,
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
			INSERT INTO codex_roots(path, kind, source, is_present, first_seen_at, last_seen_at, last_scanned_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(path) DO UPDATE SET
				kind = excluded.kind,
				source = excluded.source,
				is_present = excluded.is_present,
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
		if session.Runtime == "" {
			continue
		}
		if strings.TrimSpace(session.SessionID) == "" {
			session.SessionID = firstNonEmpty(session.NativeSessionID, session.LastResponseID)
		}
		if strings.TrimSpace(session.SessionID) == "" {
			continue
		}
		updatedAt := session.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO runtime_sessions(session_id, runtime, project_id, native_session_id, last_response_id, resume_mode, last_run_id, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, '', ?)
			ON CONFLICT(session_id, runtime) DO UPDATE SET
				project_id = COALESCE(NULLIF(excluded.project_id, ''), runtime_sessions.project_id),
				native_session_id = COALESCE(NULLIF(excluded.native_session_id, ''), runtime_sessions.native_session_id),
				last_response_id = COALESCE(NULLIF(excluded.last_response_id, ''), runtime_sessions.last_response_id),
				resume_mode = COALESCE(NULLIF(excluded.resume_mode, ''), runtime_sessions.resume_mode),
				updated_at = excluded.updated_at
		`, session.SessionID, session.Runtime, session.ProjectID, session.NativeSessionID, session.LastResponseID, session.ResumeMode, updatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
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

func stableLocalProjectID(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return "project_" + hex.EncodeToString(sum[:8])
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

func nullableTimeString(value time.Time, valid bool) sql.NullString {
	if !valid {
		return sql.NullString{}
	}
	return timeString(value)
}

func projectionID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "prj_" + hex.EncodeToString(sum[:])[:32]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func now() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
