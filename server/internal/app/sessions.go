package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

const dashSessionTTL = 12 * time.Hour

type DashPrincipal struct {
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
	Admin    bool   `json:"admin,omitempty"`
}

type SessionService struct {
	redis *redis.Client
	repo  BindRepository
	log   *slog.Logger
}

func NewSessionService(redisClient *redis.Client, repo BindRepository, logger *slog.Logger) SessionService {
	if logger == nil {
		logger = slog.Default()
	}
	return SessionService{redis: redisClient, repo: repo, log: logger}
}

func (s SessionService) Login(ctx context.Context, username, password string) (DashPrincipal, string, time.Time, error) {
	user, err := s.repo.AuthenticateUser(ctx, username, password)
	if err != nil {
		return DashPrincipal{}, "", time.Time{}, err
	}
	token, err := randomSessionToken()
	if err != nil {
		return DashPrincipal{}, "", time.Time{}, err
	}
	principal := DashPrincipal{UserID: user.UserID, Username: user.Username}
	raw, err := json.Marshal(principal)
	if err != nil {
		return DashPrincipal{}, "", time.Time{}, err
	}
	expiresAt := time.Now().UTC().Add(dashSessionTTL)
	if err := s.redis.Set(ctx, dashSessionKey(token), raw, dashSessionTTL).Err(); err != nil {
		return DashPrincipal{}, "", time.Time{}, err
	}
	return principal, token, expiresAt, nil
}

func (s SessionService) Principal(ctx context.Context, token string) (DashPrincipal, error) {
	raw, err := s.redis.Get(ctx, dashSessionKey(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return DashPrincipal{}, errors.New("dash session expired")
	}
	if err != nil {
		return DashPrincipal{}, err
	}
	var principal DashPrincipal
	if err := json.Unmarshal(raw, &principal); err != nil {
		return DashPrincipal{}, err
	}
	if principal.UserID == "" && !principal.Admin {
		return DashPrincipal{}, errors.New("invalid dash session")
	}
	_ = s.redis.Expire(ctx, dashSessionKey(token), dashSessionTTL).Err()
	return principal, nil
}

func dashSessionKey(token string) string {
	return "dash:session:" + token
}

func randomSessionToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
