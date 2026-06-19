package app

import (
	"context"
	"errors"

	"ov-computeruse/server/internal/store"
)

type BindService struct {
	repo      BindRepository
	sub2api   Sub2APIAuthenticator
	serverURL string
}

func NewBindService(repo BindRepository, sub2api Sub2APIAuthenticator, serverURL string) BindService {
	return BindService{repo: repo, sub2api: sub2api, serverURL: serverURL}
}

func (s BindService) Bind(ctx context.Context, username, password string, device store.DeviceProfile, credential store.Credential) (store.AgentIdentity, error) {
	identity, err := s.repo.AuthenticateAndBind(ctx, username, password, device, credential, s.serverURL)
	if err == nil {
		return identity, nil
	}
	if !s.shouldSyncBeforeBind(err) {
		return store.AgentIdentity{}, err
	}
	if _, syncErr := s.sub2api.SyncUser(ctx, s.repo, username, password); syncErr != nil {
		return store.AgentIdentity{}, syncErr
	}
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

func (s BindService) shouldSyncBeforeBind(err error) bool {
	return errors.Is(err, store.ErrInvalidCredentials) || errors.Is(err, store.ErrCredentialDenied)
}
