package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ov-computeruse/server/internal/security"
	"ov-computeruse/server/internal/store"
)

const sub2APILoginTimeout = 12 * time.Second

type Sub2APIAuthenticator struct {
	baseURL string
	client  *http.Client
}

type sub2APILoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sub2APILoginResponse struct {
	User sub2APIUser  `json:"user"`
	Keys []sub2APIKey `json:"keys"`
}

type sub2APIUser struct {
	ID       string `json:"id"`
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

type sub2APIKey struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	BaseURL            string `json:"base_url"`
	APIKey             string `json:"api_key"`
	Key                string `json:"key"`
	KeyFingerprint     string `json:"key_fingerprint"`
	BaseURLFingerprint string `json:"base_url_fingerprint"`
	Provider           string `json:"provider"`
	Model              string `json:"model"`
}

func NewSub2APIAuthenticator(baseURL string) Sub2APIAuthenticator {
	return Sub2APIAuthenticator{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: sub2APILoginTimeout},
	}
}

func (a Sub2APIAuthenticator) Login(ctx context.Context, repo Repository, username, password string) (DashPrincipal, error) {
	if a.baseURL == "" {
		return DashPrincipal{}, errors.New("sub2api login upstream is not configured")
	}
	response, err := a.requestLogin(ctx, username, password)
	if err != nil {
		return DashPrincipal{}, err
	}
	userID := firstNonBlank(response.User.UserID, response.User.ID, "usr_"+security.FingerprintSecret(username)[:20])
	upserted, err := repo.UpsertUser(ctx, store.UserUpsert{
		ID:       userID,
		Username: firstNonBlank(response.User.Username, username),
		Password: password,
		Actor:    "sub2api",
	})
	if err != nil {
		return DashPrincipal{}, err
	}
	if len(response.Keys) == 0 {
		return DashPrincipal{}, errors.New("sub2api returned no keys")
	}
	for _, key := range response.Keys {
		if strings.TrimSpace(key.BaseURL) == "" || keyFingerprint(key) == "" {
			return DashPrincipal{}, errors.New("sub2api returned incomplete key")
		}
		keyID := firstNonBlank(key.ID, "key_"+security.FingerprintSecret(upserted.ID, key.BaseURL, keyFingerprint(key))[:20])
		if _, err := repo.UpsertUserKey(ctx, store.UserKeyUpsert{
			ID:             keyID,
			UserID:         upserted.ID,
			Name:           key.Name,
			BaseURL:        key.BaseURL,
			KeyFingerprint: keyFingerprint(key),
			Provider:       key.Provider,
			Model:          key.Model,
			Actor:          "sub2api",
		}); err != nil {
			return DashPrincipal{}, err
		}
	}
	return DashPrincipal{UserID: upserted.ID, Username: upserted.Username}, nil
}

func (a Sub2APIAuthenticator) requestLogin(ctx context.Context, username, password string) (sub2APILoginResponse, error) {
	body, err := json.Marshal(sub2APILoginRequest{Username: username, Password: password})
	if err != nil {
		return sub2APILoginResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(a.baseURL, "/api/login"), bytes.NewReader(body))
	if err != nil {
		return sub2APILoginResponse{}, err
	}
	request.Header.Set("content-type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return sub2APILoginResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sub2APILoginResponse{}, fmt.Errorf("sub2api login rejected: status %d", response.StatusCode)
	}
	var payload sub2APILoginResponse
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		return sub2APILoginResponse{}, err
	}
	return payload, nil
}

func keyFingerprint(key sub2APIKey) string {
	if strings.TrimSpace(key.KeyFingerprint) != "" {
		return strings.TrimSpace(key.KeyFingerprint)
	}
	return security.FingerprintSecret(firstNonBlank(key.APIKey, key.Key))
}

func joinURL(baseURL, path string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + path
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String()
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
