package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCodexRuntimeSettings(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "agent.toml")
	if err := os.WriteFile(configPath, []byte("codex_model = \"gpt-5.1-codex-max\"\ncodex_profile = \"work\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(Options{
		Args: []string{"--agent-config", configPath, "--codex-profile", "override"},
		Env: map[string]string{
			envKey("CONFIG_DIR"):  filepath.Join(t.TempDir(), "config"),
			envKey("DATA_DIR"):    filepath.Join(t.TempDir(), "data"),
			envKey("CODEX_MODEL"): "gpt-5.2-codex",
		},
	})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.CodexModel != "gpt-5.2-codex" {
		t.Fatalf("codex model = %q, want env override", cfg.CodexModel)
	}
	if cfg.CodexProfile != "override" {
		t.Fatalf("codex profile = %q, want flag override", cfg.CodexProfile)
	}
}
