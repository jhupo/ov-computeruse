package installer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ov-computeruse/agent/internal/securestore"
	"ov-computeruse/agent/internal/security"
)

type Credential struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Source      string `json:"source,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

type DeviceProfile struct {
	InstallID    string `json:"install_id"`
	MachineHash  string `json:"machine_hash"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	UsernameHash string `json:"username_hash,omitempty"`
	AgentVersion string `json:"agent_version"`
}

type BindRequest struct {
	ServerKeyID string                    `json:"server_key_id"`
	Payload     security.EncryptedPayload `json:"payload"`
}

type BindPlaintext struct {
	Username    string        `json:"username"`
	Password    string        `json:"password"`
	Device      DeviceProfile `json:"device"`
	Credential  Credential    `json:"credential"`
	RequestedAt time.Time     `json:"requested_at"`
	Nonce       string        `json:"nonce"`
}

type BindResponse struct {
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	DeviceID    string `json:"device_id"`
	AgentSecret string `json:"agent_secret"`
	ServerURL   string `json:"server_url"`
	ServerKeyID string `json:"server_key_id"`
}

type errorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type Binder struct {
	ServerURL       string
	ServerKeyID     string
	ServerPublicKey string
	HTTPClient      *http.Client
}

func (b Binder) Bind(ctx context.Context, username, password string, device DeviceProfile, credential Credential) (securestore.Identity, error) {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return securestore.Identity{}, errors.New("username and password are required")
	}
	if strings.TrimSpace(b.ServerURL) == "" {
		return securestore.Identity{}, errors.New("server url is required")
	}
	if err := requireSecureServerURL(b.ServerURL); err != nil {
		return securestore.Identity{}, err
	}
	plaintext := BindPlaintext{
		Username:    username,
		Password:    password,
		Device:      device,
		Credential:  credential,
		RequestedAt: time.Now().UTC(),
	}
	nonce, err := bindNonce()
	if err != nil {
		return securestore.Identity{}, err
	}
	plaintext.Nonce = nonce
	raw, err := json.Marshal(plaintext)
	if err != nil {
		return securestore.Identity{}, err
	}
	encrypted, err := security.EncryptForServer(b.ServerKeyID, b.ServerPublicKey, raw)
	if err != nil {
		return securestore.Identity{}, err
	}
	reqBody, err := json.Marshal(BindRequest{ServerKeyID: b.ServerKeyID, Payload: encrypted})
	if err != nil {
		return securestore.Identity{}, err
	}
	client := b.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(b.ServerURL, "/")+"/api/agents/bind", bytes.NewReader(reqBody))
	if err != nil {
		return securestore.Identity{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return securestore.Identity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var body errorResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Error.Message != "" {
			return securestore.Identity{}, errors.New(body.Error.Message)
		}
		return securestore.Identity{}, errors.New(resp.Status)
	}
	var bindResp BindResponse
	if err := json.NewDecoder(resp.Body).Decode(&bindResp); err != nil {
		return securestore.Identity{}, err
	}
	serverURL := firstNonEmpty(bindResp.ServerURL, b.ServerURL)
	if err := requireSecureServerURL(serverURL); err != nil {
		return securestore.Identity{}, err
	}
	if err := validateBindResponse(bindResp, serverURL, b.ServerKeyID); err != nil {
		return securestore.Identity{}, err
	}
	return securestore.Identity{
		AgentID:     bindResp.AgentID,
		WorkspaceID: bindResp.WorkspaceID,
		DeviceID:    bindResp.DeviceID,
		AgentSecret: bindResp.AgentSecret,
		ServerURL:   serverURL,
		ServerKeyID: firstNonEmpty(bindResp.ServerKeyID, b.ServerKeyID),
	}, nil
}

func bindNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func validateBindResponse(response BindResponse, serverURL, fallbackKeyID string) error {
	missing := []string{}
	if strings.TrimSpace(response.AgentID) == "" {
		missing = append(missing, "agent_id")
	}
	if strings.TrimSpace(response.WorkspaceID) == "" {
		missing = append(missing, "workspace_id")
	}
	if strings.TrimSpace(response.DeviceID) == "" {
		missing = append(missing, "device_id")
	}
	if strings.TrimSpace(response.AgentSecret) == "" {
		missing = append(missing, "agent_secret")
	}
	if strings.TrimSpace(serverURL) == "" {
		missing = append(missing, "server_url")
	}
	if strings.TrimSpace(firstNonEmpty(response.ServerKeyID, fallbackKeyID)) == "" {
		missing = append(missing, "server_key_id")
	}
	if len(missing) > 0 {
		return errors.New("bind response missing " + strings.Join(missing, ", "))
	}
	return nil
}

func requireSecureServerURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("server url must use https")
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
