package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/term"

	"ov-computeruse/agent/internal/buildinfo"
	"ov-computeruse/agent/internal/codexscan"
	"ov-computeruse/agent/internal/config"
	"ov-computeruse/agent/internal/device"
	"ov-computeruse/agent/internal/installer"
	"ov-computeruse/agent/internal/localstate"
	"ov-computeruse/agent/internal/logging"
	"ov-computeruse/agent/internal/runs"
	"ov-computeruse/agent/internal/runtime"
	openairuntime "ov-computeruse/agent/internal/runtime/openai"
	"ov-computeruse/agent/internal/securestore"
	"ov-computeruse/agent/internal/security"
	"ov-computeruse/agent/internal/transport"
)

type loginFile struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "install":
		runInstall(os.Args[2:])
	case "run":
		runAgent(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func runInstall(args []string) {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var username, password, loginPath string
	cfg, err := config.Load(config.Options{})
	fatalIf(slog.Default(), err)
	fs.StringVar(&cfg.ServerURL, "server-url", firstNonEmpty(buildinfo.ServerURL, cfg.ServerURL), "server url")
	fs.StringVar(&cfg.ServerKeyID, "server-key-id", buildinfo.ServerKeyID, "server public key id")
	fs.StringVar(&cfg.ServerPublicKey, "server-public-key", firstNonEmpty(decodedPublicKey(), buildinfo.ServerPublicKey, cfg.ServerPublicKey), "server public key pem")
	fs.StringVar(&cfg.ConfigDir, "config-dir", cfg.ConfigDir, "agent config directory")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "agent data directory")
	fs.StringVar(&cfg.StatePath, "state", cfg.StatePath, "identity state path")
	fs.StringVar(&cfg.StateDBPath, "state-db", cfg.StateDBPath, "local state database path")
	fs.StringVar(&cfg.CodexHome, "codex-home", cfg.CodexHome, "codex home override")
	fs.Var((*stringList)(&cfg.ScanRoots), "scan-root", "local Codex root to scan; repeatable")
	fs.Int64Var(&cfg.ScanMaxBytes, "scan-max-bytes", cfg.ScanMaxBytes, "maximum file size considered by scanner")
	fs.BoolVar(&cfg.AllowSensitive, "allow-sensitive", cfg.AllowSensitive, "include paths that match sensitive-file filters")
	fs.StringVar(&username, "username", os.Getenv("OV_USERNAME"), "login username")
	fs.StringVar(&password, "password", os.Getenv("OV_PASSWORD"), "login password")
	fs.StringVar(&loginPath, "login-file", "", "json file containing login username and password")
	_ = fs.Parse(args)
	config.ApplyDerivedPaths(&cfg, explicitPathOverrides(fs))
	cfg.ConfigDir = cleanPath(cfg.ConfigDir)
	cfg.DataDir = cleanPath(cfg.DataDir)
	cfg.StatePath = cleanPath(cfg.StatePath)
	cfg.StateDBPath = cleanPath(cfg.StateDBPath)
	cfg.CodexHome = cleanPath(cfg.CodexHome)
	cfg.ScanRoots = cleanPaths(cfg.ScanRoots)

	if loginPath != "" {
		login, err := readLoginFile(loginPath)
		fatalIf(slog.Default(), err)
		if username == "" {
			username = login.Username
		}
		if password == "" {
			password = login.Password
		}
	}

	reader := bufio.NewReader(os.Stdin)
	if username == "" {
		username = prompt(reader, "Username: ")
	}
	if password == "" {
		password = promptSecret("Password: ")
	}

	ctx := context.Background()
	fatalIf(slog.Default(), config.EnsureDirs(cfg))
	logger, cleanup, err := logging.New(cfg.LogDir, cfg.LogLevel)
	fatalIf(slog.Default(), err)
	defer cleanup()
	store, err := securestore.New(cfg.StatePath)
	fatalIf(logger, err)
	fatalIf(logger, verifyPublicKeyFingerprint(cfg.ServerPublicKey))

	deviceProfile, err := device.LoadOrCreateProfile(cfg.ConfigDir, buildinfo.Version)
	fatalIf(logger, err)
	scanner := newScanner(cfg)
	credential, err := scanner.Credential()
	fatalIf(logger, err)

	identity, err := installer.Binder{
		ServerURL:       cfg.ServerURL,
		ServerKeyID:     cfg.ServerKeyID,
		ServerPublicKey: cfg.ServerPublicKey,
	}.Bind(ctx, username, password, installer.DeviceProfile{
		InstallID:    deviceProfile.InstallID,
		MachineHash:  deviceProfile.MachineHash,
		Hostname:     deviceProfile.Hostname,
		OS:           deviceProfile.OS,
		Arch:         deviceProfile.Arch,
		UsernameHash: deviceProfile.UsernameHash,
		AgentVersion: deviceProfile.AgentVersion,
	}, installer.Credential{
		BaseURL:     credential.BaseURL,
		APIKey:      credential.APIKey,
		Model:       credential.Model,
		Provider:    credential.Provider,
		Source:      credential.Source,
		Fingerprint: credential.Fingerprint,
	})
	fatalIf(logger, err)
	fatalIf(logger, store.SaveIdentity(identity))
	state, err := localstate.Open(cfg.StateDBPath)
	fatalIf(logger, err)
	defer state.Close()
	fatalIf(logger, state.SaveCodexRoots(ctx, scanner.DiscoverRoots()))
	logger.Info("agent installed", "agent_id", identity.AgentID, "device_id", identity.DeviceID, "state", store.Path())
}

func runAgent(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	cfg, err := config.Load(config.Options{})
	fatalIf(slog.Default(), err)
	fs.StringVar(&cfg.ConfigDir, "config-dir", cfg.ConfigDir, "agent config directory")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "agent data directory")
	fs.StringVar(&cfg.StatePath, "state", cfg.StatePath, "identity state path")
	fs.StringVar(&cfg.StateDBPath, "state-db", cfg.StateDBPath, "local state database path")
	fs.StringVar(&cfg.CodexHome, "codex-home", cfg.CodexHome, "codex home override")
	fs.Var((*stringList)(&cfg.ScanRoots), "scan-root", "local Codex root to scan; repeatable")
	fs.Int64Var(&cfg.ScanMaxBytes, "scan-max-bytes", cfg.ScanMaxBytes, "maximum file size considered by scanner")
	fs.BoolVar(&cfg.DisableScan, "disable-scan", cfg.DisableScan, "disable local Codex scan")
	fs.BoolVar(&cfg.UploadHistory, "upload-history", cfg.UploadHistory, "upload raw Codex history chunks to server")
	fs.BoolVar(&cfg.AllowSensitive, "allow-sensitive", cfg.AllowSensitive, "include paths that match sensitive-file filters")
	_ = fs.Parse(args)
	config.ApplyDerivedPaths(&cfg, explicitPathOverrides(fs))
	cfg.ConfigDir = cleanPath(cfg.ConfigDir)
	cfg.DataDir = cleanPath(cfg.DataDir)
	cfg.StatePath = cleanPath(cfg.StatePath)
	cfg.StateDBPath = cleanPath(cfg.StateDBPath)
	cfg.CodexHome = cleanPath(cfg.CodexHome)
	cfg.ScanRoots = cleanPaths(cfg.ScanRoots)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fatalIf(slog.Default(), config.EnsureDirs(cfg))
	logger, cleanup, err := logging.New(cfg.LogDir, cfg.LogLevel)
	fatalIf(slog.Default(), err)
	defer cleanup()
	store, err := securestore.New(cfg.StatePath)
	fatalIf(logger, err)
	identity, err := store.LoadIdentity()
	if errors.Is(err, securestore.ErrNotFound) {
		logger.Error("agent is not installed; run `agent install` first")
		os.Exit(2)
	}
	fatalIf(logger, err)

	scanner := newScanner(cfg)
	deviceProfile, err := device.LoadOrCreateProfile(cfg.ConfigDir, buildinfo.Version)
	fatalIf(logger, err)
	state, err := localstate.Open(cfg.StateDBPath)
	fatalIf(logger, err)
	defer state.Close()
	rt := runtime.Runtime(runtime.NewNoop())
	if credential, err := scanner.Credential(); err == nil {
		rt = openairuntime.New(openairuntime.Config{
			BaseURL: credential.BaseURL,
			APIKey:  credential.APIKey,
			Model:   credential.Model,
			Scanner: scanner,
		})
	} else {
		logger.Warn("codex credential not found; runtime is noop", "error", err)
	}
	manager := runs.NewManager(rt, nil, logger)
	client := transport.NewClient(identity, manager, scanner, deviceProfile, state, cfg.DisableScan, cfg.UploadHistory, logger)
	fatalIf(logger, client.Run(ctx))
}

func prompt(reader *bufio.Reader, label string) string {
	fmt.Print(label)
	value, _ := reader.ReadString('\n')
	return strings.TrimSpace(value)
}

func promptSecret(label string) string {
	fmt.Print(label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		data, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	reader := bufio.NewReader(os.Stdin)
	return prompt(reader, "")
}

func readLoginFile(path string) (loginFile, error) {
	defer func() {
		_ = os.Remove(path)
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		return loginFile{}, err
	}
	data, err = decodeLoginBytes(data)
	if err != nil {
		return loginFile{}, err
	}
	var login loginFile
	if err := json.Unmarshal(data, &login); err != nil {
		return loginFile{}, err
	}
	return login, nil
}

func decodeLoginBytes(data []byte) ([]byte, error) {
	if bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return data[3:], nil
	}
	if bytes.HasPrefix(data, []byte{0xFF, 0xFE}) {
		return decodeUTF16(data[2:], binary.LittleEndian)
	}
	if bytes.HasPrefix(data, []byte{0xFE, 0xFF}) {
		return decodeUTF16(data[2:], binary.BigEndian)
	}
	if len(data) >= 2 && data[0] == '{' && data[1] == 0 {
		return decodeUTF16(data, binary.LittleEndian)
	}
	if len(data) >= 2 && data[0] == 0 && data[1] == '{' {
		return decodeUTF16(data, binary.BigEndian)
	}
	return data, nil
}

func decodeUTF16(data []byte, order binary.ByteOrder) ([]byte, error) {
	if len(data)%2 != 0 {
		return nil, errors.New("invalid utf-16 login file")
	}
	codepoints := make([]uint16, len(data)/2)
	for i := range codepoints {
		codepoints[i] = order.Uint16(data[i*2:])
	}
	return []byte(string(utf16.Decode(codepoints))), nil
}

func newScanner(cfg config.Config) codexscan.Scanner {
	scanner := codexscan.NewScanner(cfg.CodexHome)
	scanner.Roots = cfg.ScanRoots
	scanner.MaxFileBytes = cfg.ScanMaxBytes
	scanner.AllowSensitive = cfg.AllowSensitive
	return scanner
}

func cleanPath(path string) string {
	if path == "" {
		return ""
	}
	if expanded, ok := strings.CutPrefix(path, "~"); ok && (expanded == "" || strings.HasPrefix(expanded, string(os.PathSeparator))) {
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
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' }) {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			*l = append(*l, trimmed)
		}
	}
	return nil
}

func explicitPathOverrides(fs *flag.FlagSet) map[string]bool {
	explicit := map[string]bool{}
	envToFlag := map[string]string{
		"OV_AGENT_STATE_PATH":        "state",
		"OV_AGENT_STATE_DB_PATH":     "state-db",
		"OV_AGENT_AGENT_CONFIG_PATH": "agent-config",
		"OV_AGENT_LOG_DIR":           "log-dir",
		"OV_AGENT_CACHE_DIR":         "cache-dir",
	}
	for env, flagName := range envToFlag {
		if os.Getenv(env) != "" {
			explicit[flagName] = true
		}
	}
	if fs != nil {
		fs.Visit(func(f *flag.Flag) {
			explicit[f.Name] = true
		})
	}
	return explicit
}

func fatalIf(logger *slog.Logger, err error) {
	if err == nil {
		return
	}
	logger.Error("agent failed", "error", err)
	os.Exit(1)
}

func usage() {
	fmt.Println("usage: agent <install|run> [flags]")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func decodedPublicKey() string {
	if buildinfo.ServerPublicKeyBase64 == "" {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(buildinfo.ServerPublicKeyBase64)
	if err != nil {
		return ""
	}
	return string(data)
}

func verifyPublicKeyFingerprint(publicKey string) error {
	expected := strings.TrimSpace(buildinfo.ServerPublicKeyFingerprint)
	if expected == "" || strings.TrimSpace(publicKey) == "" {
		return nil
	}
	actual, err := security.PublicKeyFingerprint(publicKey)
	if err != nil {
		return err
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("server public key fingerprint mismatch")
	}
	return nil
}
