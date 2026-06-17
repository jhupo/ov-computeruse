package device

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

const installIDFile = "install_id"

type Info struct {
	InstallID    string `json:"install_id"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	UsernameHash string `json:"username_hash"`
	MachineHash  string `json:"machine_hash"`
	AgentVersion string `json:"agent_version"`
}

type Options struct {
	ConfigDir string
	Salt      string
	Version   string
}

func Collect(opts Options) (Info, error) {
	hostname, _ := os.Hostname()
	username := currentUsername()
	installID, err := LoadOrCreateInstallID(opts.ConfigDir)
	if err != nil {
		return Info{}, err
	}
	salt := opts.Salt
	if salt == "" {
		salt = installID
	}

	return Info{
		InstallID:    installID,
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		UsernameHash: hashParts(salt, "username", username),
		MachineHash:  hashParts(salt, "machine", hostname, runtime.GOOS, runtime.GOARCH, installID),
		AgentVersion: opts.Version,
	}, nil
}

func LoadOrCreateProfile(configDir, version string) (Info, error) {
	return Collect(Options{ConfigDir: configDir, Salt: configDir, Version: version})
}

func LoadOrCreateInstallID(configDir string) (string, error) {
	if configDir == "" {
		return "", errors.New("config dir is required")
	}
	path := filepath.Join(configDir, installIDFile)
	if data, err := os.ReadFile(path); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id, nil
		}
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", err
	}
	id, err := newInstallID()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

func newInstallID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[0:4]) + "-" +
		hex.EncodeToString(b[4:6]) + "-" +
		hex.EncodeToString(b[6:8]) + "-" +
		hex.EncodeToString(b[8:10]) + "-" +
		hex.EncodeToString(b[10:16]), nil
}

func currentUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, key := range []string{"USER", "USERNAME", "LOGNAME"} {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func hashParts(salt string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(salt))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}
