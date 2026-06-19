package securestore

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

type Store struct {
	path string
}

type Identity struct {
	AgentID     string `json:"agent_id"`
	WorkspaceID string `json:"workspace_id"`
	DeviceID    string `json:"device_id"`
	AgentSecret string `json:"agent_secret"`
	ServerURL   string `json:"server_url"`
}

func New(path string) (*Store, error) {
	if path == "" {
		var err error
		path, err = defaultPath()
		if err != nil {
			return nil, err
		}
	}
	return &Store{path: path}, nil
}

func (s *Store) LoadIdentity() (Identity, error) {
	data, err := readPrivateFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Identity{}, ErrNotFound
		}
		return Identity{}, err
	}
	var identity Identity
	if err := json.Unmarshal(data, &identity); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func (s *Store) SaveIdentity(identity Identity) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return err
	}
	return writePrivateFile(s.path, data)
}

func (s *Store) Path() string {
	return s.path
}

var ErrNotFound = errors.New("identity not found")

func defaultPath() (string, error) {
	switch runtime.GOOS {
	case "windows":
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, "ov-computeruse", "agent", "identity.json"), nil
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "ov-computeruse", "agent", "config", "identity.json"), nil
		}
	default:
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, "ov-computeruse", "agent", "identity.json"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "ov-computeruse", "agent", "identity.json"), nil
}
