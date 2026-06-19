package config

import (
	"errors"
	"net/url"
	"os"
	"strings"
	"time"
)

type Config struct {
	Addr                 string
	LogLevel             string
	PublicURL            string
	Sub2APILoginUpstream string
	PostgresURL          string
	RedisURL             string
	Token                string
	DashToken            string
	BindUsersJSON        string
	ReadTimeout          time.Duration
	WriteTimeout         time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		Addr:                 firstEnv("OV_SERVER_ADDR", ":8080"),
		LogLevel:             firstEnv("OV_SERVER_LOG_LEVEL", "info"),
		PublicURL:            strings.TrimRight(os.Getenv("OV_SERVER_PUBLIC_URL"), "/"),
		Sub2APILoginUpstream: strings.TrimRight(os.Getenv("OV_SERVER_SUB2API_LOGIN_UPSTREAM"), "/"),
		PostgresURL:          os.Getenv("OV_SERVER_POSTGRES_URL"),
		RedisURL:             firstEnv("OV_SERVER_REDIS_URL", "redis://localhost:6379/0"),
		Token:                os.Getenv("OV_COMPUTERUSE_TOKEN"),
		DashToken:            os.Getenv("OV_SERVER_DASH_TOKEN"),
		BindUsersJSON:        os.Getenv("OV_SERVER_BIND_USERS_JSON"),
		ReadTimeout:          15 * time.Second,
		WriteTimeout:         15 * time.Second,
	}
	if cfg.PublicURL == "" {
		return Config{}, errors.New("OV_SERVER_PUBLIC_URL is required")
	}
	parsed, err := url.Parse(cfg.PublicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return Config{}, errors.New("OV_SERVER_PUBLIC_URL must be https")
	}
	if strings.TrimSpace(cfg.PostgresURL) == "" {
		return Config{}, errors.New("OV_SERVER_POSTGRES_URL is required")
	}
	if strings.TrimSpace(cfg.Sub2APILoginUpstream) == "" {
		return Config{}, errors.New("OV_SERVER_SUB2API_LOGIN_UPSTREAM is required")
	}
	parsedSub2API, err := url.Parse(cfg.Sub2APILoginUpstream)
	if err != nil || (parsedSub2API.Scheme != "https" && parsedSub2API.Scheme != "http") || parsedSub2API.Host == "" {
		return Config{}, errors.New("OV_SERVER_SUB2API_LOGIN_UPSTREAM must be http or https")
	}
	if strings.TrimSpace(cfg.RedisURL) == "" {
		return Config{}, errors.New("OV_SERVER_REDIS_URL is required")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return Config{}, errors.New("OV_COMPUTERUSE_TOKEN is required")
	}
	return cfg, nil
}

func firstEnv(key, fallback string) string {
	if value := os.Getenv(key); strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
