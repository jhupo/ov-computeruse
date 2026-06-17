package store

import (
	"context"

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
		_, err := s.pool.Exec(ctx, `INSERT INTO codex_sessions (agent_id, id, project_id, title, path, updated_at, size_bytes, content_sha256, indexed_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now()) ON CONFLICT (agent_id, id) DO UPDATE SET project_id=EXCLUDED.project_id, title=EXCLUDED.title, path=EXCLUDED.path, updated_at=EXCLUDED.updated_at, size_bytes=EXCLUDED.size_bytes, content_sha256=EXCLUDED.content_sha256, indexed_at=now()`, agentID, session.ID, session.ProjectID, session.Title, session.Path, session.UpdatedAt, session.Size, session.ContentSHA256)
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
