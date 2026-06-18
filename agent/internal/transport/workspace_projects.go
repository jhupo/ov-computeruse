package transport

import (
	"context"

	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/workspace"
)

type workspaceProjectSource struct {
	state *localstate.Store
}

func (s workspaceProjectSource) Projects(ctx context.Context) ([]workspace.Project, error) {
	if s.state == nil {
		return nil, nil
	}
	records, err := s.state.Projects(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]workspace.Project, 0, len(records))
	for _, record := range records {
		projects = append(projects, workspace.Project{ID: record.ID, Path: record.Path})
	}
	return projects, nil
}
