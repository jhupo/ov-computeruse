package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
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
	"ov-computeruse/agent/internal/runtime/codexcli"
	"ov-computeruse/agent/internal/securestore"
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
	cfg, err := config.Load(config.Options{Args: installConfigArgs(args)})
	fatalIf(slog.Default(), err)
	cfg.ServerURL = firstNonEmpty(cfg.ServerURL, buildinfo.ServerURL)
	cfg.Token = firstNonEmpty(cfg.Token, buildinfo.Token)
	username, password, loginPath := installLoginArgs(args)

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

	deviceProfile, err := device.LoadOrCreateProfile(cfg.ConfigDir, buildinfo.Version)
	fatalIf(logger, err)
	scanner := newScanner(cfg)
	credential, err := scanner.Credential()
	fatalIf(logger, err)

	identity, err := installer.Binder{
		ServerURL: cfg.ServerURL,
		Token:     cfg.Token,
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

func installLoginArgs(args []string) (string, string, string) {
	username := os.Getenv("OV_USERNAME")
	password := os.Getenv("OV_PASSWORD")
	loginPath := ""
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, value, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(key, "-") {
			continue
		}
		key = strings.TrimLeft(key, "-")
		switch key {
		case "username":
			value, i = flagValue(args, i, value, hasValue)
			username = value
		case "password":
			value, i = flagValue(args, i, value, hasValue)
			password = value
		case "login-file":
			value, i = flagValue(args, i, value, hasValue)
			loginPath = value
		}
	}
	return username, password, loginPath
}

func installConfigArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		key, _, hasValue := strings.Cut(arg, "=")
		if !strings.HasPrefix(key, "-") {
			out = append(out, arg)
			continue
		}
		switch strings.TrimLeft(key, "-") {
		case "username", "password", "login-file":
			if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		default:
			out = append(out, arg)
		}
	}
	return out
}

func flagValue(args []string, index int, inline string, hasInline bool) (string, int) {
	if hasInline {
		return inline, index
	}
	if index+1 < len(args) && !strings.HasPrefix(args[index+1], "-") {
		return args[index+1], index + 1
	}
	return "", index
}

func runAgent(args []string) {
	cfg, err := config.Load(config.Options{Args: args})
	fatalIf(slog.Default(), err)

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
	if bin, err := codexcli.ResolveBin(""); err == nil {
		rt = codexcli.New(codexcli.Config{
			BinPath:        bin,
			Model:          cfg.CodexModel,
			Profile:        cfg.CodexProfile,
			State:          state,
			IndexRefresher: codexIndexRefresher{scanner: scanner, state: state},
		})
		logger.Info("codex cli runtime enabled", "path", bin, "model", cfg.CodexModel, "profile", cfg.CodexProfile)
	} else {
		logger.Warn("codex cli runtime unavailable; runtime is noop", "error", err)
	}
	manager := runs.NewManager(rt, nil, logger)
	manager.SetMaxActive(cfg.MaxConcurrentRuns)
	manager.SetAckStore(state)
	client := transport.NewClient(identity, manager, scanner, deviceProfile, cfg, state, cfg.DisableScan, cfg.UploadHistory, logger)
	fatalIf(logger, client.Run(ctx))
}

type codexIndexRefresher struct {
	scanner codexscan.Scanner
	state   *localstate.Store
}

func (r codexIndexRefresher) RefreshCodexIndex(ctx context.Context) error {
	if r.state == nil {
		return nil
	}
	result, err := r.scanner.Scan(ctx)
	if err != nil {
		return err
	}
	_, err = r.state.SaveScanResult(ctx, result)
	return err
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
