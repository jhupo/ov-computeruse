package config

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const EnvPrefix = "OV_AGENT"

type Config struct {
	ServerURL       string
	ServerKeyID     string
	ServerPublicKey string
	ConfigDir       string
	DataDir         string
	StatePath       string
	StateDBPath     string
	AgentConfigPath string
	LogDir          string
	CacheDir        string
	CodexHome       string
	LogLevel        string
	ScanRoots       []string
	ScanMaxBytes    int64
	ScanTimeout     time.Duration
	DeviceSalt      string
	DisableScan     bool
	UploadHistory   bool
	AllowSensitive  bool
}

type Options struct {
	Args   []string
	Env    map[string]string
	Lookup func(string) (string, bool)
}

func Load(opts Options) (Config, error) {
	cfg := Defaults()
	explicit := map[string]bool{}
	lookup := opts.Lookup
	if lookup == nil {
		if opts.Env != nil {
			lookup = func(key string) (string, bool) {
				value, ok := opts.Env[key]
				return value, ok
			}
		} else {
			lookup = os.LookupEnv
		}
	}

	applyEnv(&cfg, lookup, explicit)

	configPath := cfg.AgentConfigPath
	if value, ok := lookup(envKey("AGENT_CONFIG_PATH")); ok {
		configPath = value
	}
	if configPath != "" {
		_ = applyConfigFile(&cfg, configPath)
	}
	applyEnv(&cfg, lookup, explicit)

	fs := flag.NewFlagSet("ov-agent", flag.ContinueOnError)
	fs.StringVar(&cfg.ServerURL, "server-url", cfg.ServerURL, "server base url")
	fs.StringVar(&cfg.ServerKeyID, "server-key-id", cfg.ServerKeyID, "server public key id")
	fs.StringVar(&cfg.ServerPublicKey, "server-public-key", cfg.ServerPublicKey, "server public key pem")
	fs.StringVar(&cfg.ConfigDir, "config-dir", cfg.ConfigDir, "agent config directory")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "agent data directory")
	fs.StringVar(&cfg.StatePath, "state", cfg.StatePath, "identity state path")
	fs.StringVar(&cfg.StateDBPath, "state-db", cfg.StateDBPath, "local state database path")
	fs.StringVar(&cfg.AgentConfigPath, "agent-config", cfg.AgentConfigPath, "local agent config path")
	fs.StringVar(&cfg.LogDir, "log-dir", cfg.LogDir, "agent log directory")
	fs.StringVar(&cfg.CacheDir, "cache-dir", cfg.CacheDir, "agent cache directory")
	fs.StringVar(&cfg.CodexHome, "codex-home", cfg.CodexHome, "codex home override")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level")
	fs.Var((*stringList)(&cfg.ScanRoots), "scan-root", "local Codex root to scan; repeatable")
	fs.Int64Var(&cfg.ScanMaxBytes, "scan-max-bytes", cfg.ScanMaxBytes, "maximum file size considered by scanner")
	fs.DurationVar(&cfg.ScanTimeout, "scan-timeout", cfg.ScanTimeout, "scanner timeout")
	fs.StringVar(&cfg.DeviceSalt, "device-salt", cfg.DeviceSalt, "local salt for device hashes")
	fs.BoolVar(&cfg.DisableScan, "disable-scan", cfg.DisableScan, "disable local Codex scan")
	fs.BoolVar(&cfg.UploadHistory, "upload-history", cfg.UploadHistory, "upload raw Codex history chunks to server")
	fs.BoolVar(&cfg.AllowSensitive, "allow-sensitive", cfg.AllowSensitive, "include paths that match sensitive-file filters")

	if len(opts.Args) > 0 {
		if err := fs.Parse(opts.Args); err != nil {
			return Config{}, err
		}
		fs.Visit(func(f *flag.Flag) {
			explicit[f.Name] = true
		})
	}

	ApplyDerivedPaths(&cfg, explicit)
	cfg.ConfigDir = cleanPath(cfg.ConfigDir)
	cfg.DataDir = cleanPath(cfg.DataDir)
	cfg.StatePath = cleanPath(cfg.StatePath)
	cfg.StateDBPath = cleanPath(cfg.StateDBPath)
	cfg.AgentConfigPath = cleanPath(cfg.AgentConfigPath)
	cfg.LogDir = cleanPath(cfg.LogDir)
	cfg.CacheDir = cleanPath(cfg.CacheDir)
	cfg.CodexHome = cleanPath(cfg.CodexHome)
	cfg.ScanRoots = cleanPaths(cfg.ScanRoots)
	if cfg.DeviceSalt == "" {
		cfg.DeviceSalt = cfg.ConfigDir
	}
	return cfg, nil
}

func applyConfigFile(cfg *Config, path string) error {
	data, err := os.ReadFile(cleanPath(path))
	if err != nil {
		return err
	}
	values, err := parseConfigFile(data)
	if err != nil {
		return err
	}
	for key, value := range values {
		applyConfigValue(cfg, key, value)
	}
	return nil
}

func parseConfigFile(data []byte) (map[string]string, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return map[string]string{}, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var raw map[string]any
		if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
			return nil, err
		}
		out := map[string]string{}
		flattenJSONConfig("", raw, out)
		return out, nil
	}
	return parseFlatTOML(trimmed), nil
}

func flattenJSONConfig(prefix string, values map[string]any, out map[string]string) {
	for key, value := range values {
		fullKey := key
		if prefix != "" {
			fullKey = prefix + "." + key
		}
		switch typed := value.(type) {
		case string:
			out[fullKey] = typed
		case bool:
			out[fullKey] = strconv.FormatBool(typed)
		case float64:
			out[fullKey] = strconv.FormatInt(int64(typed), 10)
		case []any:
			parts := make([]string, 0, len(typed))
			for _, item := range typed {
				if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
					parts = append(parts, text)
				}
			}
			out[fullKey] = strings.Join(parts, ",")
		case map[string]any:
			flattenJSONConfig(fullKey, typed, out)
		}
	}
}

func parseFlatTOML(data string) map[string]string {
	out := map[string]string{}
	section := ""
	for _, line := range strings.Split(data, "\n") {
		line = stripComment(strings.TrimSpace(line))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if section != "" {
			key = section + "." + key
		}
		out[key] = parseConfigScalar(strings.TrimSpace(value))
	}
	return out
}

func stripComment(line string) string {
	inQuote := false
	for i, r := range line {
		switch r {
		case '"':
			inQuote = !inQuote
		case '#':
			if !inQuote {
				return strings.TrimSpace(line[:i])
			}
		}
	}
	return line
}

func parseConfigScalar(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		value = strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
		parts := []string{}
		for _, part := range strings.Split(value, ",") {
			parts = append(parts, strings.Trim(strings.TrimSpace(part), `"`))
		}
		return strings.Join(parts, ",")
	}
	return strings.Trim(value, `"`)
}

func applyConfigValue(cfg *Config, key, value string) {
	switch normalizeConfigKey(key) {
	case "server_url":
		cfg.ServerURL = value
	case "server_key_id":
		cfg.ServerKeyID = value
	case "server_public_key":
		cfg.ServerPublicKey = value
	case "config_dir":
		cfg.ConfigDir = value
	case "data_dir":
		cfg.DataDir = value
	case "state_path", "state":
		cfg.StatePath = value
	case "state_db_path", "state_db":
		cfg.StateDBPath = value
	case "agent_config_path", "agent_config":
		cfg.AgentConfigPath = value
	case "log_dir":
		cfg.LogDir = value
	case "cache_dir":
		cfg.CacheDir = value
	case "codex_home":
		cfg.CodexHome = value
	case "log_level":
		cfg.LogLevel = value
	case "scan_roots", "scan_root":
		cfg.ScanRoots = splitList(value)
	case "scan_max_bytes":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			cfg.ScanMaxBytes = parsed
		}
	case "scan_timeout":
		if parsed, err := time.ParseDuration(value); err == nil {
			cfg.ScanTimeout = parsed
		}
	case "device_salt":
		cfg.DeviceSalt = value
	case "disable_scan":
		cfg.DisableScan = parseBool(value)
	case "upload_history":
		cfg.UploadHistory = parseBool(value)
	case "allow_sensitive":
		cfg.AllowSensitive = parseBool(value)
	}
}

func normalizeConfigKey(key string) string {
	key = strings.TrimSpace(strings.ToLower(key))
	key = strings.TrimPrefix(key, "agent.")
	key = strings.ReplaceAll(key, "-", "_")
	key = strings.ReplaceAll(key, ".", "_")
	return key
}

func ApplyDerivedPaths(cfg *Config, explicit map[string]bool) {
	if cfg == nil {
		return
	}
	if explicit == nil {
		explicit = map[string]bool{}
	}
	if !explicit["state"] && !explicit["state-path"] {
		cfg.StatePath = filepath.Join(cfg.ConfigDir, "identity.json")
	}
	if !explicit["state-db"] && !explicit["state-db-path"] {
		cfg.StateDBPath = filepath.Join(cfg.DataDir, "state.db")
	}
	if !explicit["agent-config"] && !explicit["agent-config-path"] {
		cfg.AgentConfigPath = filepath.Join(cfg.ConfigDir, "agent.toml")
	}
	if !explicit["log-dir"] {
		cfg.LogDir = filepath.Join(cfg.DataDir, "logs")
	}
	if !explicit["cache-dir"] {
		cfg.CacheDir = filepath.Join(cfg.DataDir, "cache")
	}
}

func Defaults() Config {
	configDir, dataDir := defaultDirs()
	return Config{
		ConfigDir:       configDir,
		DataDir:         dataDir,
		StatePath:       filepath.Join(configDir, "identity.json"),
		StateDBPath:     filepath.Join(dataDir, "state.db"),
		AgentConfigPath: filepath.Join(configDir, "agent.toml"),
		LogDir:          filepath.Join(dataDir, "logs"),
		CacheDir:        filepath.Join(dataDir, "cache"),
		CodexHome:       os.Getenv("CODEX_HOME"),
		LogLevel:        "info",
		ScanMaxBytes:    4 << 20,
		ScanTimeout:     30 * time.Second,
	}
}

func Default() Config {
	return Defaults()
}

func EnsureDirs(cfg Config) error {
	for _, dir := range []string{cfg.ConfigDir, cfg.DataDir, cfg.LogDir, cfg.CacheDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func applyEnv(cfg *Config, lookup func(string) (string, bool), explicit map[string]bool) {
	if value, ok := lookup(envKey("SERVER_URL")); ok {
		cfg.ServerURL = value
	}
	if value, ok := lookup(envKey("SERVER_KEY_ID")); ok {
		cfg.ServerKeyID = value
	}
	if value, ok := lookup(envKey("SERVER_PUBLIC_KEY")); ok {
		cfg.ServerPublicKey = value
	}
	if value, ok := lookup(envKey("CONFIG_DIR")); ok {
		cfg.ConfigDir = value
		explicit["config-dir"] = true
	}
	if value, ok := lookup(envKey("DATA_DIR")); ok {
		cfg.DataDir = value
		explicit["data-dir"] = true
	}
	if value, ok := lookup(envKey("STATE_PATH")); ok {
		cfg.StatePath = value
		explicit["state"] = true
	}
	if value, ok := lookup(envKey("STATE_DB_PATH")); ok {
		cfg.StateDBPath = value
		explicit["state-db"] = true
	}
	if value, ok := lookup(envKey("AGENT_CONFIG_PATH")); ok {
		cfg.AgentConfigPath = value
		explicit["agent-config"] = true
	}
	if value, ok := lookup(envKey("LOG_DIR")); ok {
		cfg.LogDir = value
		explicit["log-dir"] = true
	}
	if value, ok := lookup(envKey("CACHE_DIR")); ok {
		cfg.CacheDir = value
		explicit["cache-dir"] = true
	}
	if value, ok := lookup("CODEX_HOME"); ok {
		cfg.CodexHome = value
	}
	if value, ok := lookup(envKey("CODEX_HOME")); ok {
		cfg.CodexHome = value
	}
	if value, ok := lookup(envKey("LOG_LEVEL")); ok {
		cfg.LogLevel = value
	}
	if value, ok := lookup(envKey("SCAN_ROOTS")); ok {
		cfg.ScanRoots = splitList(value)
	}
	if value, ok := lookup(envKey("SCAN_MAX_BYTES")); ok {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			cfg.ScanMaxBytes = parsed
		}
	}
	if value, ok := lookup(envKey("SCAN_TIMEOUT")); ok {
		if parsed, err := time.ParseDuration(value); err == nil {
			cfg.ScanTimeout = parsed
		}
	}
	if value, ok := lookup(envKey("DEVICE_SALT")); ok {
		cfg.DeviceSalt = value
	}
	if value, ok := lookup(envKey("DISABLE_SCAN")); ok {
		cfg.DisableScan = parseBool(value)
	}
	if value, ok := lookup(envKey("UPLOAD_HISTORY")); ok {
		cfg.UploadHistory = parseBool(value)
	}
	if value, ok := lookup(envKey("ALLOW_SENSITIVE")); ok {
		cfg.AllowSensitive = parseBool(value)
	}
}

func envKey(name string) string {
	return EnvPrefix + "_" + name
}

func defaultDirs() (string, string) {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		base := firstEnv("APPDATA", home)
		local := firstEnv("LOCALAPPDATA", base)
		return filepath.Join(base, "ov-computeruse", "agent"), filepath.Join(local, "ov-computeruse", "agent")
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support", "ov-computeruse", "agent")
		return filepath.Join(base, "config"), filepath.Join(base, "data")
	default:
		configBase := firstEnv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		dataBase := firstEnv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
		return filepath.Join(configBase, "ov-computeruse", "agent"), filepath.Join(dataBase, "ov-computeruse", "agent")
	}
}

func firstEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func parseBool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "t", "true", "y", "yes", "on":
		return true
	default:
		return false
	}
}

func splitList(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	if expanded, ok := strings.CutPrefix(path, "~"); ok && (expanded == "" || strings.HasPrefix(expanded, string(filepath.Separator))) {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, expanded)
		}
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func cleanPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		cleaned := cleanPath(path)
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}

type stringList []string

func (l *stringList) String() string {
	return strings.Join(*l, ",")
}

func (l *stringList) Set(value string) error {
	for _, item := range splitList(value) {
		*l = append(*l, item)
	}
	return nil
}
