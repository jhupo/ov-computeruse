package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ov-computeruse/server/internal/store"
)

type sub2APITestRepo struct {
	user store.UserUpsert
	keys []store.UserKeyUpsert
}

func (r *sub2APITestRepo) UpsertUser(_ context.Context, input store.UserUpsert) (store.UserRecord, error) {
	r.user = input
	return store.UserRecord{ID: input.ID, Username: input.Username}, nil
}

func (r *sub2APITestRepo) UpsertUserKey(_ context.Context, input store.UserKeyUpsert) (store.UserKeyRecord, error) {
	r.keys = append(r.keys, input)
	return store.UserKeyRecord{ID: input.ID, UserID: input.UserID, Name: input.Name, BaseURL: input.BaseURL, KeyFingerprint: input.KeyFingerprint, Provider: input.Provider, Model: input.Model}, nil
}

func TestSub2APILoginSyncsGatewayKeys(t *testing.T) {
	var loginPath, loginEmail, keyAuth string
	gatewayBaseURL := "https://gateway.example/v1"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			loginPath = r.URL.Path
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode login body: %v", err)
			}
			loginEmail = body["email"]
			if _, ok := body["username"]; ok {
				t.Fatal("sub2api login request must use email, not username")
			}
			writeSub2APITestJSON(w, map[string]any{
				"access_token":  "access-token",
				"refresh_token": "refresh-token",
				"expires_in":    3600,
				"token_type":    "Bearer",
				"user": map[string]any{
					"id":       42,
					"email":    "user@example.com",
					"username": "demo",
					"balance":  12.5,
				},
			})
		case "/api/v1/keys":
			keyAuth = r.Header.Get("authorization")
			if r.URL.Query().Get("page_size") != "100" {
				t.Fatalf("page_size = %q, want 100", r.URL.Query().Get("page_size"))
			}
			writeSub2APITestJSON(w, map[string]any{
				"items": []map[string]any{
					{"id": 7, "name": "codex", "key": "sk-live", "status": "active", "group": map[string]any{"platform": "openai"}},
					{"id": 8, "name": "off", "key": "sk-disabled", "status": "inactive"},
				},
			})
		case "/api/v1/settings/public":
			writeSub2APITestJSON(w, map[string]any{"api_base_url": gatewayBaseURL})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	repo := &sub2APITestRepo{}
	principal, err := NewSub2APIAuthenticator(upstream.URL).Login(context.Background(), repo, "user@example.com", "secret")
	if err != nil {
		t.Fatalf("login failed: %v", err)
	}

	if loginPath != "/api/v1/auth/login" || loginEmail != "user@example.com" {
		t.Fatalf("login request path/email = %q/%q", loginPath, loginEmail)
	}
	if keyAuth != "Bearer access-token" {
		t.Fatalf("key authorization = %q", keyAuth)
	}
	if principal.UserID != "42" || principal.Username != "user@example.com" {
		t.Fatalf("principal = %+v", principal)
	}
	if repo.user.ID != "42" || repo.user.Username != "user@example.com" || repo.user.Actor != "sub2api" {
		t.Fatalf("upsert user = %+v", repo.user)
	}
	if repo.user.Sub2APIAccessToken != "access-token" || repo.user.Sub2APIRefreshToken != "refresh-token" || repo.user.Sub2APITokenType != "Bearer" {
		t.Fatalf("sub2api token fields = %+v", repo.user)
	}
	if repo.user.Sub2APITokenExpiresAt == nil || repo.user.Sub2APIBalance == nil || *repo.user.Sub2APIBalance != 12.5 {
		t.Fatalf("sub2api state fields = %+v", repo.user)
	}
	if len(repo.keys) != 1 {
		t.Fatalf("synced keys = %d, want 1", len(repo.keys))
	}
	key := repo.keys[0]
	if key.ID != "7" || key.UserID != "42" || key.Name != "codex" || key.BaseURL != gatewayBaseURL || key.Provider != "openai" {
		t.Fatalf("synced key = %+v", key)
	}
	if key.KeyFingerprint != credentialFingerprint(gatewayBaseURL, "sk-live") {
		t.Fatalf("key fingerprint mismatch")
	}
	if strings.Contains(key.KeyFingerprint, "sk-live") {
		t.Fatal("key fingerprint leaked raw key")
	}
}

func TestBindServiceSyncsSub2APIThenRetriesCredentialBind(t *testing.T) {
	repo := &bindRetryRepo{bindErrs: []error{store.ErrCredentialDenied, nil}}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSub2APITestJSON(w, map[string]any{
				"access_token": "access-token",
				"token_type":   "Bearer",
				"user": map[string]any{
					"id":    42,
					"email": "user@example.com",
				},
			})
		case "/api/v1/keys":
			writeSub2APITestJSON(w, map[string]any{
				"items": []map[string]any{{"id": 7, "name": "codex", "key": "sk-live", "status": "active"}},
			})
		case "/api/v1/settings/public":
			writeSub2APITestJSON(w, map[string]any{"api_base_url": "https://gateway.example/v1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	identity, err := NewBindService(repo, NewSub2APIAuthenticator(upstream.URL), "https://computer.example").
		Bind(context.Background(), "user@example.com", "secret", store.DeviceProfile{}, store.Credential{BaseURL: "https://gateway.example/v1", Fingerprint: credentialFingerprint("https://gateway.example/v1", "sk-live")})
	if err != nil {
		t.Fatalf("bind failed: %v", err)
	}
	if identity.AgentID != "agt_1" {
		t.Fatalf("identity = %+v", identity)
	}
	if repo.bindCalls != 2 {
		t.Fatalf("bind calls = %d, want 2", repo.bindCalls)
	}
	if repo.user.ID != "42" || len(repo.keys) != 1 {
		t.Fatalf("sub2api sync did not upsert user/key: user=%+v keys=%+v", repo.user, repo.keys)
	}
}

type bindRetryRepo struct {
	sub2APITestRepo
	bindCalls int
	bindErrs  []error
}

func (r *bindRetryRepo) AuthenticateAndBind(context.Context, string, string, store.DeviceProfile, store.Credential, string) (store.AgentIdentity, error) {
	r.bindCalls++
	if len(r.bindErrs) > 0 {
		err := r.bindErrs[0]
		r.bindErrs = r.bindErrs[1:]
		if err != nil {
			return store.AgentIdentity{}, err
		}
	}
	return store.AgentIdentity{AgentID: "agt_1"}, nil
}

func (r *bindRetryRepo) AuthenticateUser(context.Context, string, string) (store.UserIdentity, error) {
	return store.UserIdentity{}, errors.New("unexpected AuthenticateUser call")
}

func (r *bindRetryRepo) EnsureBindUser(context.Context, store.BindUser) error {
	return errors.New("unexpected EnsureBindUser call")
}

func writeSub2APITestJSON(w http.ResponseWriter, data any) {
	w.Header().Set("content-type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    0,
		"message": "success",
		"data":    data,
	})
}
