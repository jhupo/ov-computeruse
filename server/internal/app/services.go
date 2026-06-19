package app

import (
	"context"

	"ov-computeruse/server/internal/store"
)

type BindService struct {
	repo      BindRepository
	serverURL string
}

func NewBindService(repo BindRepository, serverURL string) BindService {
	return BindService{repo: repo, serverURL: serverURL}
}

func (s BindService) Bind(ctx context.Context, username, password string, device store.DeviceProfile, credential store.Credential) (store.AgentIdentity, error) {
	return s.repo.AuthenticateAndBind(ctx, username, password, device, credential, s.serverURL)
}

func (s BindService) SeedUsers(ctx context.Context, users []store.BindUser) error {
	for _, user := range users {
		if err := s.repo.EnsureBindUser(ctx, user); err != nil {
			return err
		}
	}
	return nil
}
