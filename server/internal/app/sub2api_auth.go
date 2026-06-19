package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	Email    string `json:"email"`
	Password string `json:"password"`
}

type sub2APILoginResponse struct {
	AccessToken  string      `json:"access_token"`
	RefreshToken string      `json:"refresh_token"`
	ExpiresIn    int         `json:"expires_in"`
	TokenType    string      `json:"token_type"`
	User         sub2APIUser `json:"user"`
}

type sub2APIUser struct {
	ID       json.Number `json:"id"`
	Email    string      `json:"email"`
	Username string      `json:"username"`
	Balance  float64     `json:"balance"`
}

type sub2APIKey struct {
	ID     json.Number   `json:"id"`
	Name   string        `json:"name"`
	Key    string        `json:"key"`
	Status string        `json:"status"`
	Group  *sub2APIGroup `json:"group"`
}

type sub2APIGroup struct {
	Platform string `json:"platform"`
}

type sub2APIEnvelope[T any] struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type sub2APIPage[T any] struct {
	Items []T `json:"items"`
}

type sub2APISettings struct {
	APIBaseURL string `json:"api_base_url"`
}

type sub2APIRepository interface {
	UpsertUser(context.Context, store.UserUpsert) (store.UserRecord, error)
	UpsertUserKey(context.Context, store.UserKeyUpsert) (store.UserKeyRecord, error)
}

func NewSub2APIAuthenticator(baseURL string) Sub2APIAuthenticator {
	return Sub2APIAuthenticator{
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client:  &http.Client{Timeout: sub2APILoginTimeout},
	}
}

func (a Sub2APIAuthenticator) Login(ctx context.Context, repo sub2APIRepository, username, password string) (DashPrincipal, error) {
	record, err := a.SyncUser(ctx, repo, username, password)
	if err != nil {
		return DashPrincipal{}, err
	}
	return DashPrincipal{UserID: record.ID, Username: record.Username}, nil
}

func (a Sub2APIAuthenticator) SyncUser(ctx context.Context, repo sub2APIRepository, username, password string) (store.UserRecord, error) {
	if a.baseURL == "" {
		return store.UserRecord{}, errors.New("sub2api login upstream is not configured")
	}
	login, err := a.requestLogin(ctx, username, password)
	if err != nil {
		return store.UserRecord{}, err
	}
	keys, err := a.requestKeys(ctx, login.AccessToken)
	if err != nil {
		return store.UserRecord{}, err
	}
	baseURL, err := a.requestGatewayBaseURL(ctx)
	if err != nil {
		return store.UserRecord{}, err
	}
	userID := firstNonBlank(login.User.ID.String(), "usr_"+security.FingerprintSecret(username)[:20])
	expiresAt := tokenExpiresAt(login.ExpiresIn)
	balance := login.User.Balance
	upserted, err := repo.UpsertUser(ctx, store.UserUpsert{
		ID:                    userID,
		Username:              firstNonBlank(login.User.Email, login.User.Username, username),
		Password:              password,
		Sub2APIAccessToken:    login.AccessToken,
		Sub2APIRefreshToken:   login.RefreshToken,
		Sub2APITokenType:      firstNonBlank(login.TokenType, "Bearer"),
		Sub2APITokenExpiresAt: expiresAt,
		Sub2APIBalance:        &balance,
		Actor:                 "sub2api",
	})
	if err != nil {
		return store.UserRecord{}, err
	}
	if len(keys) == 0 {
		return store.UserRecord{}, errors.New("sub2api returned no keys")
	}
	for _, key := range keys {
		if strings.TrimSpace(key.Key) == "" {
			return store.UserRecord{}, errors.New("sub2api returned incomplete key")
		}
		keyID := firstNonBlank(key.ID.String(), "key_"+security.FingerprintSecret(upserted.ID, baseURL, key.Key)[:20])
		if _, err := repo.UpsertUserKey(ctx, store.UserKeyUpsert{
			ID:             keyID,
			UserID:         upserted.ID,
			Name:           key.Name,
			BaseURL:        baseURL,
			KeyFingerprint: credentialFingerprint(baseURL, key.Key),
			Provider:       keyProvider(key),
			Actor:          "sub2api",
		}); err != nil {
			return store.UserRecord{}, err
		}
	}
	return upserted, nil
}

func (a Sub2APIAuthenticator) requestLogin(ctx context.Context, username, password string) (sub2APILoginResponse, error) {
	body, err := json.Marshal(sub2APILoginRequest{Email: username, Password: password})
	if err != nil {
		return sub2APILoginResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, joinURL(a.baseURL, "/api/v1/auth/login"), bytes.NewReader(body))
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
	payload, err := decodeSub2API[sub2APILoginResponse](response)
	if err != nil {
		return sub2APILoginResponse{}, err
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return sub2APILoginResponse{}, errors.New("sub2api login returned no access token")
	}
	return payload, nil
}

func (a Sub2APIAuthenticator) requestKeys(ctx context.Context, accessToken string) ([]sub2APIKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(a.baseURL, "/api/v1/keys?page=1&page_size=100"), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("authorization", "Bearer "+strings.TrimSpace(accessToken))
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("sub2api keys rejected: status %d", response.StatusCode)
	}
	page, err := decodeSub2API[sub2APIPage[sub2APIKey]](response)
	if err != nil {
		return nil, err
	}
	active := make([]sub2APIKey, 0, len(page.Items))
	for _, key := range page.Items {
		if strings.TrimSpace(key.Status) == "" || strings.EqualFold(strings.TrimSpace(key.Status), "active") {
			active = append(active, key)
		}
	}
	return active, nil
}

func (a Sub2APIAuthenticator) requestGatewayBaseURL(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, joinURL(a.baseURL, "/api/v1/settings/public"), nil)
	if err != nil {
		return "", err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("sub2api settings rejected: status %d", response.StatusCode)
	}
	settings, err := decodeSub2API[sub2APISettings](response)
	if err != nil {
		return "", err
	}
	return firstNonBlank(settings.APIBaseURL, a.gatewayBaseURL()), nil
}

func decodeSub2API[T any](response *http.Response) (T, error) {
	var envelope sub2APIEnvelope[T]
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		var zero T
		return zero, err
	}
	if envelope.Code != 0 {
		var zero T
		return zero, fmt.Errorf("sub2api rejected: code %d message %s", envelope.Code, envelope.Message)
	}
	return envelope.Data, nil
}

func credentialFingerprint(baseURL, apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimRight(strings.ToLower(baseURL), "/") + "\x00" + apiKey))
	return hex.EncodeToString(sum[:])
}

func keyProvider(key sub2APIKey) string {
	if key.Group == nil {
		return ""
	}
	return strings.TrimSpace(key.Group.Platform)
}

func joinURL(baseURL, path string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return baseURL + path
	}
	relative, err := url.Parse(path)
	if err != nil {
		parsed.Path = strings.TrimRight(parsed.Path, "/") + path
		return parsed.String()
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + relative.Path
	parsed.RawQuery = relative.RawQuery
	return parsed.String()
}

func (a Sub2APIAuthenticator) gatewayBaseURL() string {
	return joinURL(a.baseURL, "/v1")
}

func tokenExpiresAt(expiresIn int) *time.Time {
	if expiresIn <= 0 {
		return nil
	}
	value := time.Now().UTC().Add(time.Duration(expiresIn) * time.Second)
	return &value
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
