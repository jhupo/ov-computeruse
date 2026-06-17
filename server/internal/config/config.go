package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"

	"ov-computeruse/server/internal/buildinfo"
)

type Config struct {
	Addr                       string
	LogLevel                   string
	PublicURL                  string
	PostgresURL                string
	RedisURL                   string
	ServerKeyID                string
	ServerPrivateKeyPEM        string
	ServerPublicKeyFingerprint string
	DashToken                  string
	BindUsersJSON              string
	ReadTimeout                time.Duration
	WriteTimeout               time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Addr:                       firstEnv("OV_SERVER_ADDR", ":8080"),
		LogLevel:                   firstEnv("OV_SERVER_LOG_LEVEL", "info"),
		PublicURL:                  strings.TrimRight(os.Getenv("OV_SERVER_PUBLIC_URL"), "/"),
		PostgresURL:                os.Getenv("OV_SERVER_POSTGRES_URL"),
		RedisURL:                   firstEnv("OV_SERVER_REDIS_URL", "redis://localhost:6379/0"),
		ServerKeyID:                firstNonEmpty(os.Getenv("OV_SERVER_KEY_ID"), buildinfo.ServerKeyID),
		ServerPrivateKeyPEM:        os.Getenv("OV_SERVER_PRIVATE_KEY_PEM"),
		ServerPublicKeyFingerprint: firstNonEmpty(os.Getenv("OV_SERVER_PUBLIC_KEY_FINGERPRINT"), buildinfo.ServerPublicKeyFingerprint),
		DashToken:                  os.Getenv("OV_SERVER_DASH_TOKEN"),
		BindUsersJSON:              os.Getenv("OV_SERVER_BIND_USERS_JSON"),
		ReadTimeout:                15 * time.Second,
		WriteTimeout:               15 * time.Second,
	}
	if path := os.Getenv("OV_SERVER_PRIVATE_KEY_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, err
		}
		cfg.ServerPrivateKeyPEM = string(data)
	}
	if cfg.PublicURL == "" {
		return Config{}, errors.New("OV_SERVER_PUBLIC_URL is required")
	}
	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return Config{}, errors.New("OV_SERVER_PUBLIC_URL must be https")
	}
	if strings.TrimSpace(cfg.ServerKeyID) == "" {
		return Config{}, errors.New("OV_SERVER_KEY_ID is required")
	}
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		return Config{}, errors.New("OV_SERVER_POSTGRES_URL is required")
	}
	if strings.TrimSpace(cfg.RedisURL) == "" {
		return Config{}, errors.New("OV_SERVER_REDIS_URL is required")
	}
	if strings.TrimSpace(cfg.ServerPrivateKeyPEM) == "" {
		return Config{}, errors.New("OV_SERVER_PRIVATE_KEY_PEM or OV_SERVER_PRIVATE_KEY_FILE is required")
	}
	if strings.TrimSpace(cfg.DashToken) == "" {
		return Config{}, errors.New("OV_SERVER_DASH_TOKEN is required")
	}
	return cfg, nil
}

func firstEnv(key, fallback string) string {
	if value := os.Getenv(key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
