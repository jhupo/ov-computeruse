package store

import (
	"context"
	"database/sql"

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
		_, err := s.pool.Exec(ctx, `INSERT INTO projects (agent_id, id, name, path, last_active_at, has_agents_md, git_branch, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7,now()) ON CONFLICT (agent_id, id) DO UPDATE SET name=EXCLUDED.name, path=EXCLUDED.path, last_active_at=EXCLUDED.last_active_at, has_agents_md=EXCLUDED.has_agents_md, git_branch=EXCLUDED.git_branch, updated_at=now()`, agentID, project.ID, project.Name, project.Path, project.LastActiveAt, project.HasAgentsMD, project.GitBranch)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveSessions(ctx context.Context, agentID string, sessions []protocol.Session) error {
	for _, session := range sessions {
		_, err := s.pool.Exec(ctx, `INSERT INTO codex_sessions (agent_id, id, id_source, project_id, title, path, cwd, updated_at, size_bytes, content_sha256, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,now()) ON CONFLICT (agent_id, id) DO UPDATE SET id_source=EXCLUDED.id_source, project_id=EXCLUDED.project_id, title=EXCLUDED.title, path=EXCLUDED.path, cwd=EXCLUDED.cwd, updated_at=EXCLUDED.updated_at, size_bytes=EXCLUDED.size_bytes, content_sha256=EXCLUDED.content_sha256, indexed_at=now()`, agentID, session.ID, session.IDSource, session.ProjectID, session.Title, session.Path, session.CWD, session.UpdatedAt, session.Size, session.ContentSHA256)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SaveHistoryChunk(ctx context.Context, agentID string, chunk protocol.HistoryChunk) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO history_chunks (agent_id, session_id, chunk_index, sha256, size_bytes, received_at) VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT DO NOTHING`, agentID, chunk.SessionID, chunk.Index, chunk.SHA256, len(chunk.Data))
	return err
}

func (s *Store) SaveHistoryMessages(ctx context.Context, agentID string, batch protocol.HistoryMessages) error {
	for _, message := range batch.Messages {
		if message.SessionID == "" {
			message.SessionID = batch.SessionID
		}
		if message.SessionID == "" || message.Text == "" {
			continue
		}
		_, err := s.pool.Exec(ctx, `INSERT INTO history_messages (agent_id, session_id, message_index, role, text, message_at, received_at) VALUES ($1,$2,$3,$4,$5,$6,now()) ON CONFLICT (agent_id, session_id, message_index) DO UPDATE SET role=EXCLUDED.role, text=EXCLUDED.text, message_at=EXCLUDED.message_at, received_at=now()`, agentID, message.SessionID, message.Index, message.Role, message.Text, message.At)
		if err != nil {
			return err
		}
		if err := s.projectHistoryMessage(ctx, agentID, message); err != nil {
			return err
		}
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

func (s *Store) SaveSyncCursor(ctx context.Context, agentID string, cursor protocol.SyncCursor) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO sync_cursors (agent_id, stream, subject_id, cursor, cursor_at, updated_at) VALUES ($1,$2,$3,$4,$5,now()) ON CONFLICT (agent_id, stream, subject_id) DO UPDATE SET cursor=EXCLUDED.cursor, cursor_at=EXCLUDED.cursor_at, updated_at=now()`,
		agentID, cursor.Stream, cursor.SubjectID, cursor.Cursor, cursor.At)
	return err
}
