package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"

	"ov-computeruse/server/internal/protocol"
)

func (s *Store) SaveRoots(ctx context.Context, agentID string, roots []protocol.Root) error {
	for _, root := range roots {
		_, err := s.pool.Exec(ctx, `INSERT INTO codex_roots (agent_id, path, kind, source, exists, updated_at) VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT (agent_id, path) DO UPDATE SET kind=EXCLUDED.kind, source=EXCLUDED.source, exists=EXCLUDED.exists, updated_at=now()`, agentID, root.Path, root.Kind, root.Source, root.Exists)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveProjects(ctx context.Context, agentID string, projects []protocol.Project) error {
	for _, project := range projects {
		_, err := s.pool.Exec(ctx, `INSERT INTO projects (agent_id, id, name, path, last_active_at, has_agents_md, git_branch, deleted_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,NULL,now()) ON CONFLICT (agent_id, id) DO UPDATE SET name=EXCLUDED.name, path=EXCLUDED.path, last_active_at=EXCLUDED.last_active_at, has_agents_md=EXCLUDED.has_agents_md, git_branch=EXCLUDED.git_branch, deleted_at=NULL, updated_at=now()`, agentID, project.ID, project.Name, project.Path, project.LastActiveAt, project.HasAgentsMD, project.GitBranch)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveSessions(ctx context.Context, agentID string, sessions []protocol.Session) error {
	for _, session := range sessions {
		_, err := s.pool.Exec(ctx, `INSERT INTO codex_sessions (agent_id, id, id_source, project_id, title, path, cwd, updated_at, size_bytes, content_sha256, deleted_at, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULL,now()) ON CONFLICT (agent_id, id) DO UPDATE SET id_source=EXCLUDED.id_source, project_id=EXCLUDED.project_id, title=EXCLUDED.title, path=EXCLUDED.path, cwd=EXCLUDED.cwd, updated_at=EXCLUDED.updated_at, size_bytes=EXCLUDED.size_bytes, content_sha256=EXCLUDED.content_sha256, deleted_at=NULL, indexed_at=now()`, agentID, session.ID, session.IDSource, session.ProjectID, session.Title, session.Path, session.CWD, session.UpdatedAt, session.Size, session.ContentSHA256)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ProjectExists(ctx context.Context, agentID, projectID string) (bool, error) {
	if projectID == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM projects WHERE agent_id=$1 AND id=$2 AND deleted_at IS NULL)`, agentID, projectID).Scan(&exists)
	return exists, err
}

func (s *Store) SessionExists(ctx context.Context, agentID, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM codex_sessions WHERE agent_id=$1 AND id=$2 AND deleted_at IS NULL)
		OR EXISTS(SELECT 1 FROM runtime_sessions WHERE agent_id=$1 AND session_id=$2)`, agentID, sessionID).Scan(&exists)
	return exists, err
}

func (s *Store) MarkIndexDeleted(ctx context.Context, agentID string, deleted protocol.DeletedIndex) error {
	for _, project := range deleted.Projects {
		if project.ID == "" {
			continue
		}
		deletedAt := project.DeletedAt
		if deletedAt.IsZero() {
			deletedAt = now()
		}
		if _, err := s.pool.Exec(ctx, `UPDATE projects SET deleted_at=$3, updated_at=now() WHERE agent_id=$1 AND id=$2`, agentID, project.ID, deletedAt); err != nil {
			return err
		}
	}
	for _, session := range deleted.Sessions {
		if session.ID == "" {
			continue
		}
		deletedAt := session.DeletedAt
		if deletedAt.IsZero() {
			deletedAt = now()
		}
		if _, err := s.pool.Exec(ctx, `UPDATE codex_sessions SET deleted_at=$3, indexed_at=now() WHERE agent_id=$1 AND id=$2`, agentID, session.ID, deletedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveHistoryChunk(ctx context.Context, agentID string, chunk protocol.HistoryChunk) error {
	if chunk.SessionID == "" {
		return errors.New("history chunk session_id is required")
	}
	if chunk.Index < 0 {
		return errors.New("history chunk index is invalid")
	}
	if len(chunk.Data) == 0 {
		return errors.New("history chunk data is required")
	}
	exists, err := s.SessionExists(ctx, agentID, chunk.SessionID)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("history chunk session does not belong to agent")
	}
	computed := historyChunkSHA256(chunk.Data)
	if chunk.SHA256 == "" || chunk.SHA256 != computed {
		return errors.New("history chunk sha256 mismatch")
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO history_chunks (agent_id, session_id, chunk_index, sha256, size_bytes, received_at) VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT DO NOTHING`, agentID, chunk.SessionID, chunk.Index, computed, len(chunk.Data))
	return err
}

func historyChunkSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (s *Store) SaveHistoryMessages(ctx context.Context, agentID string, batch protocol.HistoryMessages) error {
	for _, message := range batch.Messages {
		if message.SessionID == "" {
			message.SessionID = batch.SessionID
		}
		if message.SessionID == "" || message.Text == "" {
			continue
		}
		if err := s.saveHistoryMessageProjection(ctx, agentID, message); err != nil {
			return err
		}
		if err := s.projectHistoryMessage(ctx, agentID, message); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveHistoryItems(ctx context.Context, agentID string, batch protocol.HistoryItems) error {
	if batch.Reset && batch.SessionID != "" {
		if _, err := s.pool.Exec(ctx, `DELETE FROM history_items WHERE agent_id=$1 AND session_id=$2 AND source='codex.history'`, agentID, batch.SessionID); err != nil {
			return err
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM history_messages WHERE agent_id=$1 AND session_id=$2`, agentID, batch.SessionID); err != nil {
			return err
		}
	}
	for _, item := range batch.Items {
		if item.SessionID == "" {
			item.SessionID = batch.SessionID
		}
		if item.SessionID == "" || item.Kind == "" {
			continue
		}
		if protocol.IsUsageKind(item.Kind) {
			continue
		}
		source := item.Source
		if source == "" {
			source = "codex.history"
		}
		if err := s.SaveHistoryItem(ctx, HistoryItem{
			AgentID:       agentID,
			SessionID:     item.SessionID,
			Index:         item.Index,
			Role:          item.Role,
			Kind:          item.Kind,
			Text:          item.Text,
			Payload:       item.Payload,
			Source:        source,
			SourceEventID: item.SourceEventID,
			At:            item.At,
		}); err != nil {
			return err
		}
		if item.Kind == "message" && item.Text != "" {
			if err := s.saveHistoryMessageProjection(ctx, agentID, protocol.HistoryMessage{
				SessionID: item.SessionID,
				Index:     item.Index,
				Role:      item.Role,
				Text:      item.Text,
				At:        item.At,
			}); err != nil {
				return err
			}
		}
	}
	if batch.Cursor != "" {
		return s.SaveSyncCursor(ctx, agentID, protocol.SyncCursor{Stream: "history.items", SubjectID: batch.SessionID, Cursor: batch.Cursor, At: now()})
	}
	return nil
}

func (s *Store) HistoryMessages(ctx context.Context, agentID, sessionID string) ([]protocol.HistoryMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT session_id, message_index, role, text, message_at FROM history_messages WHERE agent_id=$1 AND session_id=$2 ORDER BY message_index ASC`, agentID, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := []protocol.HistoryMessage{}
	for rows.Next() {
		var message protocol.HistoryMessage
		var at sql.NullTime
		if err := rows.Scan(&message.SessionID, &message.Index, &message.Role, &message.Text, &at); err != nil {
			return nil, err
		}
		if at.Valid {
			message.At = at.Time
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) saveHistoryMessageProjection(ctx context.Context, agentID string, message protocol.HistoryMessage) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO history_messages (agent_id, session_id, message_index, role, text, message_at, received_at) VALUES ($1,$2,$3,$4,$5,$6,now()) ON CONFLICT (agent_id, session_id, message_index) DO UPDATE SET role=EXCLUDED.role, text=EXCLUDED.text, message_at=EXCLUDED.message_at, received_at=now()`, agentID, message.SessionID, message.Index, message.Role, message.Text, message.At)
	return err
}

func (s *Store) SaveSyncCursor(ctx context.Context, agentID string, cursor protocol.SyncCursor) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sync_cursors (agent_id, stream, subject_id, cursor, cursor_at, updated_at) VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT (agent_id, stream, subject_id) DO UPDATE SET cursor=EXCLUDED.cursor, cursor_at=EXCLUDED.cursor_at, updated_at=now()`,
		agentID, cursor.Stream, cursor.SubjectID, cursor.Cursor, cursor.At)
	return err
}
