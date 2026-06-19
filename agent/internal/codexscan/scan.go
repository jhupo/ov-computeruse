package codexscan

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"ov-computeruse/agent/internal/protocol"
)

type Scanner struct {
	Roots          []string
	CodexHome      string
	MaxFileBytes   int64
	IncludeHidden  bool
	AllowSensitive bool
}

type Credential struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key"`
	Model       string `json:"model,omitempty"`
	Provider    string `json:"provider,omitempty"`
	Source      string `json:"source,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

type Result struct {
	Roots           []Root           `json:"roots"`
	Files           []File           `json:"files"`
	Projects        []Project        `json:"projects"`
	Sessions        []Session        `json:"sessions"`
	RuntimeSessions []RuntimeSession `json:"runtime_sessions"`
}

type Root struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Source string `json:"source,omitempty"`
	Exists bool   `json:"exists"`
}

type File struct {
	Path      string    `json:"path"`
	Root      string    `json:"root"`
	Kind      string    `json:"kind"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	SHA256    string    `json:"sha256,omitempty"`
	Sensitive bool      `json:"sensitive"`
}

type Project struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Root         string    `json:"root"`
	LastActiveAt time.Time `json:"last_active_at,omitempty"`
	HasAgentsMD  bool      `json:"has_agents_md"`
	GitBranch    string    `json:"git_branch,omitempty"`
}

type Session struct {
	ID            string    `json:"id"`
	IDSource      string    `json:"id_source,omitempty"`
	ProjectID     string    `json:"project_id,omitempty"`
	Title         string    `json:"title,omitempty"`
	Path          string    `json:"path"`
	Root          string    `json:"root,omitempty"`
	CWD           string    `json:"cwd,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	Size          int64     `json:"size,omitempty"`
	ContentSHA256 string    `json:"content_sha256,omitempty"`
}

type RuntimeSession struct {
	Runtime         string    `json:"runtime"`
	ProjectID       string    `json:"project_id,omitempty"`
	SessionID       string    `json:"session_id,omitempty"`
	NativeSessionID string    `json:"native_session_id,omitempty"`
	ResumeMode      string    `json:"resume_mode,omitempty"`
	Title           string    `json:"title,omitempty"`
	CWD             string    `json:"cwd,omitempty"`
	Model           string    `json:"model,omitempty"`
	ApprovalPolicy  string    `json:"approval_policy,omitempty"`
	SandboxMode     string    `json:"sandbox_mode,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	LastTurnID      string    `json:"last_turn_id,omitempty"`
	LastItemIndex   int       `json:"last_item_index,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type SessionMessage struct {
	Role string
	Text string
	At   time.Time
}

type HistoryItem struct {
	SessionID     string
	Index         int
	Role          string
	Kind          string
	Text          string
	Payload       json.RawMessage
	SourceEventID string
	At            time.Time
}

func NewScanner(codexHome string) Scanner {
	return Scanner{CodexHome: codexHome}
}

func (s Scanner) DiscoverRoots() []Root {
	roots := s.roots()
	out := make([]Root, 0, len(roots))
	for _, root := range roots {
		info, err := os.Stat(root)
		out = append(out, Root{
			Path:   root,
			Kind:   classifyRoot(root),
			Source: rootSource(root, s.CodexHome),
			Exists: err == nil && info.IsDir(),
		})
	}
	return out
}

func (s Scanner) Credential() (Credential, error) {
	roots := s.roots()
	candidates := make([]string, 0)
	for _, root := range roots {
		candidates = append(candidates,
			filepath.Join(root, "config.toml"),
			filepath.Join(root, "config.json"),
			filepath.Join(root, "auth.json"),
		)
	}
	envKey := firstEnv("OPENAI_API_KEY", firstEnv("OV_OPENAI_API_KEY", ""))
	envBase := firstEnv("OPENAI_BASE_URL", firstEnv("OV_OPENAI_BASE_URL", ""))
	if envKey != "" {
		cred := Credential{
			BaseURL: firstNonEmpty(envBase, "https://api.openai.com/v1"),
			APIKey:  envKey,
			Source:  "env",
		}
		cred.Fingerprint = credentialFingerprint(cred.BaseURL, cred.APIKey)
		return cred, nil
	}
	for _, path := range candidates {
		cred, ok := readCredentialFile(path)
		if !ok {
			continue
		}
		cred.Source = path
		if cred.BaseURL == "" {
			cred.BaseURL = "https://api.openai.com/v1"
		}
		cred.Fingerprint = credentialFingerprint(cred.BaseURL, cred.APIKey)
		return cred, nil
	}
	return Credential{}, errors.New("codex credential not found")
}

func DefaultRoots() []string {
	home, _ := os.UserHomeDir()
	var roots []string
	add := func(paths ...string) {
		for _, path := range paths {
			if path != "" {
				roots = append(roots, filepath.Clean(path))
			}
		}
	}

	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		add(codexHome)
	}
	add(filepath.Join(home, ".codex"))

	switch runtime.GOOS {
	case "windows":
		add(
			joinEnv("APPDATA", "Codex"),
			joinEnv("APPDATA", "codex"),
			joinEnv("LOCALAPPDATA", "Codex"),
			joinEnv("LOCALAPPDATA", "codex"),
		)
	case "darwin":
		add(
			filepath.Join(home, "Library", "Application Support", "Codex"),
			filepath.Join(home, "Library", "Application Support", "codex"),
		)
	default:
		configBase := firstEnv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
		dataBase := firstEnv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
		add(filepath.Join(configBase, "codex"), filepath.Join(dataBase, "codex"))
	}
	return uniqueClean(roots)
}

func (s Scanner) Scan(ctx context.Context) (Result, error) {
	roots := s.roots()
	maxBytes := s.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = 4 << 20
	}

	result := Result{}
	for _, root := range uniqueClean(roots) {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		default:
		}

		info, err := os.Stat(root)
		exists := err == nil && info.IsDir()
		result.Roots = append(result.Roots, Root{Path: root, Kind: classifyRoot(root), Source: rootSource(root, s.CodexHome), Exists: exists})
		if !exists {
			continue
		}
		sessionTitles := readSessionIndex(filepath.Join(root, "session_index.jsonl"))

		err = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			if path == root {
				return nil
			}
			name := entry.Name()
			if entry.IsDir() {
				if shouldSkipDir(root, path, name, s.IncludeHidden) {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return nil
			}
			kind := classifyFile(root, path)
			if kind == "" {
				return nil
			}
			sensitive := IsSensitivePath(path)
			if sensitive && !s.AllowSensitive {
				return nil
			}
			file := File{
				Path:      path,
				Root:      root,
				Kind:      kind,
				Size:      info.Size(),
				ModTime:   info.ModTime().UTC(),
				Sensitive: sensitive,
			}
			if info.Size() <= maxBytes && !sensitive {
				file.SHA256 = fileHash(path)
			}
			result.Files = append(result.Files, file)
			switch kind {
			case "project":
				result.Projects = append(result.Projects, projectFromFile(root, path, info))
			case "session", "history":
				session := sessionFromFile(path, info, maxBytes, sessionTitles)
				session.Root = root
				result.Sessions = append(result.Sessions, session)
				if runtimeSession := runtimeSessionFromFile(session); runtimeSession.SessionID != "" || runtimeSession.NativeSessionID != "" {
					result.RuntimeSessions = append(result.RuntimeSessions, runtimeSession)
				}
				if session.CWD != "" {
					result.Projects = append(result.Projects, projectFromCWD(root, session.CWD, session.UpdatedAt))
				}
			}
			return nil
		})
		if err != nil {
			return result, err
		}
	}
	sort.Slice(result.Files, func(i, j int) bool {
		return result.Files[i].Path < result.Files[j].Path
	})
	result.Projects = uniqueProjects(result.Projects)
	sort.Slice(result.Projects, func(i, j int) bool {
		return result.Projects[i].Path < result.Projects[j].Path
	})
	result.Sessions = uniqueSessions(result.Sessions)
	sort.Slice(result.Sessions, func(i, j int) bool {
		return result.Sessions[i].UpdatedAt.After(result.Sessions[j].UpdatedAt)
	})
	result.RuntimeSessions = uniqueRuntimeSessions(result.RuntimeSessions)
	sort.Slice(result.RuntimeSessions, func(i, j int) bool {
		return result.RuntimeSessions[i].UpdatedAt.After(result.RuntimeSessions[j].UpdatedAt)
	})
	return result, nil
}

func (s Scanner) HistoryChunks(ctx context.Context, session Session, chunkSize int) ([]HistoryChunk, error) {
	var chunks []HistoryChunk
	err := s.ForEachHistoryChunk(ctx, session, chunkSize, func(chunk HistoryChunk) error {
		chunks = append(chunks, chunk)
		return nil
	})
	return chunks, err
}

func (s Scanner) ForEachHistoryChunk(ctx context.Context, session Session, chunkSize int, yield func(HistoryChunk) error) error {
	if chunkSize <= 0 {
		chunkSize = 64 << 10
	}
	if IsSensitivePath(session.Path) {
		return errors.New("refusing to upload sensitive history file")
	}
	file, err := os.Open(session.Path)
	if err != nil {
		return err
	}
	defer file.Close()
	buffer := make([]byte, chunkSize)
	index := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if err := yield(HistoryChunk{
				SessionID: session.ID,
				Index:     index,
				Data:      append([]byte(nil), buffer[:n]...),
				SHA256:    bytesHash(buffer[:n]),
			}); err != nil {
				return err
			}
			index++
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	return nil
}

type HistoryChunk struct {
	SessionID string `json:"session_id"`
	Index     int    `json:"index"`
	Data      []byte `json:"data"`
	SHA256    string `json:"sha256"`
}

func (s Scanner) roots() []string {
	roots := append([]string(nil), s.Roots...)
	if s.CodexHome != "" {
		roots = append([]string{s.CodexHome}, roots...)
	}
	if len(roots) == 0 {
		roots = DefaultRoots()
	}
	return uniqueClean(roots)
}

func IsSensitivePath(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	sensitiveNames := []string{
		".env", ".netrc", "credentials", "credentials.json", "cookies", "cookies.json",
		"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "known_hosts",
		"key.pem", "cert.pem", "client.key", "client.pem",
	}
	for _, name := range sensitiveNames {
		if base == name {
			return true
		}
	}
	sensitiveExts := []string{".pem", ".key", ".p12", ".pfx", ".crt", ".cer"}
	for _, ext := range sensitiveExts {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	sensitiveParts := []string{
		"/.ssh/", "/secrets/", "/secret/", "/tokens/", "/token/", "/credentials/",
		"/keychain/", "/cookies/", "/passwords/",
	}
	for _, part := range sensitiveParts {
		if strings.Contains(normalized, part) {
			return true
		}
	}
	return false
}

func classifyRoot(path string) string {
	base := strings.ToLower(filepath.Base(path))
	if base == ".codex" || base == "codex" {
		return "codex_home"
	}
	return "codex_candidate"
}

func rootSource(path, codexHome string) string {
	cleaned := filepath.Clean(path)
	if codexHome != "" && samePath(cleaned, codexHome) {
		return "codex_home_override"
	}
	if envHome := os.Getenv("CODEX_HOME"); envHome != "" && samePath(cleaned, envHome) {
		return "CODEX_HOME"
	}
	home, _ := os.UserHomeDir()
	switch {
	case samePath(cleaned, filepath.Join(home, ".codex")):
		return "home"
	case strings.Contains(strings.ToLower(filepath.ToSlash(cleaned)), "/application support/codex"):
		return "platform_default"
	default:
		return "platform_default"
	}
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr == nil {
		left = leftAbs
	}
	if rightErr == nil {
		right = rightAbs
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(filepath.Clean(left), filepath.Clean(right))
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func classifyFile(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	relSlash := strings.ToLower(filepath.ToSlash(rel))
	base := strings.ToLower(filepath.Base(path))

	switch {
	case base == "config.toml" || base == "config.json" || base == "settings.json":
		return "config"
	case strings.HasPrefix(relSlash, "projects/"):
		return "project"
	case strings.HasPrefix(relSlash, "sessions/"), strings.HasPrefix(relSlash, "archived_sessions/"):
		return "session"
	case strings.Contains(relSlash, "/sessions/"), strings.Contains(relSlash, "/archived_sessions/"):
		return "session"
	case base == "history.jsonl" || base == "history.json" || strings.HasPrefix(relSlash, "history/"):
		return "history"
	default:
		return ""
	}
}

func readCredentialFile(path string) (Credential, bool) {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || IsSensitivePath(path) {
		return Credential{}, false
	}
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		var raw map[string]any
		if json.Unmarshal(data, &raw) == nil {
			cred := Credential{
				BaseURL:  stringField(raw, "base_url", "baseURL", "api_base", "apiBase"),
				APIKey:   stringField(raw, "api_key", "apiKey", "openai_api_key", "OPENAI_API_KEY"),
				Model:    stringField(raw, "model"),
				Provider: stringField(raw, "provider"),
			}
			return cred, cred.APIKey != ""
		}
	}
	text := string(data)
	cred := Credential{
		BaseURL:  tomlLikeValue(text, "base_url", "api_base"),
		APIKey:   tomlLikeValue(text, "api_key", "openai_api_key"),
		Model:    tomlLikeValue(text, "model"),
		Provider: tomlLikeValue(text, "provider"),
	}
	return cred, cred.APIKey != ""
}

func tomlLikeValue(text string, keys ...string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, key := range keys {
			if !strings.HasPrefix(line, key) {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) != key {
				continue
			}
			return strings.Trim(strings.TrimSpace(parts[1]), `"'`)
		}
	}
	return ""
}

func stringField(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := raw[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func credentialFingerprint(baseURL, apiKey string) string {
	return bytesHash([]byte(strings.TrimRight(strings.ToLower(baseURL), "/") + "\x00" + apiKey))
}

func projectFromFile(root, path string, info os.FileInfo) Project {
	projectPath := path
	if !info.IsDir() {
		projectPath = filepath.Dir(path)
	}
	name := filepath.Base(projectPath)
	return Project{
		ID:           stableID(projectPath),
		Name:         name,
		Path:         projectPath,
		Root:         root,
		LastActiveAt: info.ModTime().UTC(),
		HasAgentsMD:  fileExists(filepath.Join(projectPath, "AGENTS.md")),
		GitBranch:    readGitBranch(projectPath),
	}
}

func sessionFromFile(path string, info os.FileInfo, maxBytes int64, titles map[string]string) Session {
	contentHash := ""
	if maxBytes <= 0 || info.Size() <= maxBytes {
		contentHash = fileHash(path)
	}
	meta := readSessionMeta(path)
	id := meta.ID
	idSource := "codex_session_meta"
	if id == "" {
		id = sessionIDFromFilename(path)
		idSource = "filename"
	}
	if id == "" {
		id = stableID(path)
		idSource = "path_hash"
	}
	title := titles[id]
	if title == "" {
		title = firstSessionUserText(path, 96)
	}
	if title == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	projectID := ""
	if meta.CWD != "" {
		projectID = stableID(meta.CWD)
	}
	return Session{
		ID:            id,
		IDSource:      idSource,
		ProjectID:     projectID,
		Title:         title,
		Path:          path,
		CWD:           meta.CWD,
		UpdatedAt:     info.ModTime().UTC(),
		Size:          info.Size(),
		ContentSHA256: contentHash,
	}
}

type sessionMeta struct {
	ID              string
	ThreadID        string
	CWD             string
	Model           string
	ApprovalPolicy  string
	SandboxMode     string
	ReasoningEffort string
	LastTurnID      string
}

func readSessionMeta(path string) sessionMeta {
	file, err := os.Open(path)
	if err != nil {
		return sessionMeta{}
	}
	defer file.Close()
	scanner := newJSONLScanner(io.LimitReader(file, 512<<10))
	for scanner.Scan() {
		var raw struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		if raw.Type == "session_meta" {
			return sessionMetaFromPayload(raw.Payload)
		}
	}
	return sessionMeta{}
}

func runtimeSessionFromFile(session Session) RuntimeSession {
	if session.Path == "" {
		return RuntimeSession{}
	}
	file, err := os.Open(session.Path)
	if err != nil {
		return RuntimeSession{}
	}
	defer file.Close()
	scanner := newJSONLScanner(file)
	nativeSessionID := session.ID
	context := sessionMeta{CWD: session.CWD}
	updatedAt := session.UpdatedAt
	lastItemIndex := 0
	historyIndex := 0
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		var row rolloutRow
		if err := json.Unmarshal(line, &row); err != nil {
			continue
		}
		if row.Timestamp.After(updatedAt) {
			updatedAt = row.Timestamp
		}
		if item, ok := parseHistoryItem(session.ID, historyIndex, line, 256<<10); ok {
			if !(item.Kind == "message" && item.Role == "user" && looksLikeContextBlock(item.Text)) && !skipHistoryKind(item.Kind) {
				lastItemIndex = historyIndex
				historyIndex++
			}
		}
		if row.Type == "session_meta" {
			meta := sessionMetaFromPayload(row.Payload)
			if meta.ID != "" {
				nativeSessionID = meta.ID
			}
			if meta.ThreadID != "" {
				nativeSessionID = meta.ThreadID
			}
			context = mergeSessionMeta(context, meta)
			continue
		}
		if row.Type == "turn_context" || row.Type == "session_configured" {
			meta := sessionMetaFromPayload(row.Payload)
			if meta.ThreadID != "" {
				nativeSessionID = meta.ThreadID
			}
			context = mergeSessionMeta(context, meta)
			continue
		}
		var payload map[string]any
		if json.Unmarshal(row.Payload, &payload) == nil {
			payloadType := stringFromAny(payload["type"])
			if payloadType == "turn_context" || payloadType == "session_configured" {
				meta := sessionMetaFromAny(payload)
				if meta.ThreadID != "" {
					nativeSessionID = meta.ThreadID
				}
				context = mergeSessionMeta(context, meta)
			}
		}
	}
	if nativeSessionID == "" {
		return RuntimeSession{}
	}
	return RuntimeSession{
		Runtime:         protocol.RuntimeCodexCLI,
		ProjectID:       session.ProjectID,
		SessionID:       session.ID,
		NativeSessionID: nativeSessionID,
		ResumeMode:      "codex_cli_history_index",
		Title:           session.Title,
		CWD:             firstNonEmpty(context.CWD, session.CWD),
		Model:           context.Model,
		ApprovalPolicy:  context.ApprovalPolicy,
		SandboxMode:     context.SandboxMode,
		ReasoningEffort: context.ReasoningEffort,
		LastTurnID:      context.LastTurnID,
		LastItemIndex:   lastItemIndex,
		UpdatedAt:       updatedAt,
	}
}

type rolloutRow struct {
	ID        string          `json:"id"`
	Timestamp time.Time       `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func sessionMetaFromPayload(raw json.RawMessage) sessionMeta {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return sessionMeta{}
	}
	return sessionMetaFromAny(payload)
}

func sessionMetaFromAny(payload map[string]any) sessionMeta {
	if nested, ok := payload["meta"].(map[string]any); ok {
		payload = mergeStringAnyMaps(payload, nested)
	}
	return sessionMeta{
		ID:              stringFromAny(payload["id"]),
		ThreadID:        stringFromAny(payload["thread_id"]),
		CWD:             cleanExistingPath(firstNonEmpty(stringFromAny(payload["cwd"]), pathStringFromAny(payload["cwd"]))),
		Model:           firstNonEmpty(stringFromAny(payload["model"]), stringFromAny(payload["model_slug"])),
		ApprovalPolicy:  stringFromAny(payload["approval_policy"]),
		SandboxMode:     firstNonEmpty(stringFromAny(payload["sandbox_mode"]), stringFromAny(payload["permission_profile"]), stringFromAny(payload["sandbox_policy"])),
		ReasoningEffort: firstNonEmpty(stringFromAny(payload["reasoning_effort"]), stringFromAny(payload["effort"])),
		LastTurnID:      firstNonEmpty(stringFromAny(payload["turn_id"]), stringFromAny(payload["sub_id"])),
	}
}

func mergeSessionMeta(existing, incoming sessionMeta) sessionMeta {
	existing.ID = firstNonEmpty(incoming.ID, existing.ID)
	existing.ThreadID = firstNonEmpty(incoming.ThreadID, existing.ThreadID)
	existing.CWD = firstNonEmpty(incoming.CWD, existing.CWD)
	existing.Model = firstNonEmpty(incoming.Model, existing.Model)
	existing.ApprovalPolicy = firstNonEmpty(incoming.ApprovalPolicy, existing.ApprovalPolicy)
	existing.SandboxMode = firstNonEmpty(incoming.SandboxMode, existing.SandboxMode)
	existing.ReasoningEffort = firstNonEmpty(incoming.ReasoningEffort, existing.ReasoningEffort)
	existing.LastTurnID = firstNonEmpty(incoming.LastTurnID, existing.LastTurnID)
	return existing
}

func mergeStringAnyMaps(base, overlay map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(overlay))
	for key, value := range base {
		merged[key] = value
	}
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

func pathStringFromAny(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		return firstNonEmpty(stringFromAny(typed["path"]), stringFromAny(typed["display_path"]))
	default:
		return ""
	}
}

func readSessionIndex(path string) map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	titles := map[string]string{}
	scanner := newJSONLScanner(file)
	for scanner.Scan() {
		var row struct {
			ID         string `json:"id"`
			ThreadName string `json:"thread_name"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row.ID != "" && row.ThreadName != "" {
			titles[row.ID] = row.ThreadName
		}
	}
	return titles
}

func firstSessionUserText(path string, max int) string {
	messages, _ := ReadSessionMessages(context.Background(), path, 16, max*8)
	for _, message := range messages {
		if message.Role == "user" && message.Text != "" && !looksLikeContextBlock(message.Text) {
			return truncateText(message.Text, max)
		}
	}
	return ""
}

func ReadSessionMessages(ctx context.Context, path string, maxMessages int, maxBytes int) ([]SessionMessage, error) {
	if maxMessages <= 0 {
		maxMessages = 24
	}
	if maxBytes <= 0 {
		maxBytes = 24 << 10
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	scanner := newJSONLScanner(file)
	messages := make([]SessionMessage, 0, maxMessages)
	total := 0
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return messages, ctx.Err()
		default:
		}
		var row struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Payload   struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			continue
		}
		if row.Type != "response_item" || row.Payload.Type != "message" {
			continue
		}
		role := strings.TrimSpace(row.Payload.Role)
		if role != "user" && role != "assistant" {
			continue
		}
		text := messageText(row.Payload.Content)
		if text == "" {
			continue
		}
		if role == "user" && looksLikeContextBlock(text) {
			continue
		}
		text = truncateText(text, maxBytes)
		total += len(text)
		messages = append(messages, SessionMessage{Role: role, Text: text, At: row.Timestamp})
		for len(messages) > maxMessages || total > maxBytes {
			total -= len(messages[0].Text)
			messages = messages[1:]
		}
	}
	return messages, nil
}

func ReadSessionItems(ctx context.Context, session Session, maxItems int, maxBytes int) ([]HistoryItem, error) {
	if maxItems <= 0 {
		maxItems = 1000
	}
	if maxBytes <= 0 {
		maxBytes = 2 << 20
	}
	items := make([]HistoryItem, 0)
	total := 0
	err := ForEachSessionItem(ctx, session, 256<<10, func(item HistoryItem) error {
		total += len(item.Text) + len(item.Payload)
		items = append(items, item)
		for len(items) > maxItems || total > maxBytes {
			total -= len(items[0].Text) + len(items[0].Payload)
			items = items[1:]
		}
		return nil
	})
	if err != nil {
		return items, err
	}
	for i := range items {
		items[i].Index = i
	}
	return items, nil
}

func ForEachSessionItem(ctx context.Context, session Session, maxTextBytes int, yield func(HistoryItem) error) error {
	if maxTextBytes <= 0 {
		maxTextBytes = 256 << 10
	}
	if IsSensitivePath(session.Path) {
		return errors.New("refusing to parse sensitive history file")
	}
	file, err := os.Open(session.Path)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := newJSONLScanner(file)
	index := 0
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		item, ok := parseHistoryItem(session.ID, index, scanner.Bytes(), maxTextBytes)
		if !ok {
			continue
		}
		if item.Kind == "message" && item.Role == "user" && looksLikeContextBlock(item.Text) {
			continue
		}
		if err := yield(item); err != nil {
			return err
		}
		index++
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func parseHistoryItem(sessionID string, index int, rawLine []byte, maxText int) (HistoryItem, bool) {
	var row rolloutRow
	if err := json.Unmarshal(rawLine, &row); err != nil {
		return HistoryItem{}, false
	}
	if len(row.Payload) == 0 {
		row.Payload = append(json.RawMessage(nil), rawLine...)
	}
	var payload map[string]any
	_ = json.Unmarshal(row.Payload, &payload)
	payloadType := stringFromAny(payload["type"])
	kind := historyKind(row.Type, payloadType)
	if kind == "" || skipHistoryKind(kind) {
		return HistoryItem{}, false
	}
	role := stringFromAny(payload["role"])
	text := historyText(kind, payload, maxText)
	sourceID := firstNonEmpty(row.ID, stringFromAny(payload["id"]), stringFromAny(payload["call_id"]))
	return HistoryItem{
		SessionID:     sessionID,
		Index:         index,
		Role:          role,
		Kind:          kind,
		Text:          text,
		Payload:       append(json.RawMessage(nil), row.Payload...),
		SourceEventID: sourceID,
		At:            row.Timestamp,
	}, true
}

func historyKind(rowType, payloadType string) string {
	switch payloadType {
	case "message", "agent_message":
		return "message"
	case "reasoning", "reasoning_item":
		return "reasoning"
	case "function_call", "mcp_call", "local_shell_call", "code_interpreter_call", "file_search_call", "web_search_call", "computer_call",
		"command_execution", "mcp_tool_call", "file_change", "web_search", "collab_tool_call":
		return "tool.call"
	case "function_call_output", "mcp_call_output", "local_shell_call_output", "code_interpreter_call_output", "file_search_call_output", "web_search_call_output", "computer_call_output":
		return "tool.output"
	case "todo_list":
		return "todo.list"
	case "error":
		return "error"
	case "mcp_approval_request", "exec_approval_request", "apply_patch_approval_request", "elicitation_request":
		return "approval.requested"
	case "mcp_approval_response":
		return "approval.resolved"
	}
	switch rowType {
	case "session_meta":
		return "session.meta"
	case "turn_context":
		return "session.context"
	case "response_item":
		if payloadType != "" {
			return payloadType
		}
	case "event_msg", "event":
		if payloadType != "" {
			return payloadType
		}
	}
	return ""
}

func skipHistoryKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "usage", "response.usage", "token_usage", "billing", "cost":
		return true
	default:
		return false
	}
}

func historyText(kind string, payload map[string]any, max int) string {
	switch kind {
	case "message":
		return truncateText(firstNonEmpty(stringFromAny(payload["text"]), messageTextFromAny(payload["content"])), max)
	case "reasoning":
		return truncateText(firstNonEmpty(messageTextFromAny(payload["summary"]), messageTextFromAny(payload["content"]), stringFromAny(payload["text"])), max)
	case "tool.call":
		return truncateText(firstNonEmpty(
			stringFromAny(payload["name"]),
			stringFromAny(payload["tool_name"]),
			stringFromAny(payload["tool"]),
			stringFromAny(payload["command"]),
			messageTextFromAny(payload["changes"]),
			messageTextFromAny(payload["output"]),
			messageTextFromAny(payload["result"]),
			stringFromAny(payload["arguments"]),
			compactJSONText(payload["arguments"]),
			compactJSONText(payload["action"]),
			compactJSONText(payload["changes"]),
		), max)
	case "tool.output":
		return truncateText(firstNonEmpty(
			messageTextFromAny(payload["output"]),
			messageTextFromAny(payload["result"]),
			stringFromAny(payload["error"]),
			compactJSONText(payload["output"]),
			compactJSONText(payload["result"]),
			compactJSONText(payload["error"]),
		), max)
	case "approval.requested":
		return truncateText(firstNonEmpty(stringFromAny(payload["command"]), stringFromAny(payload["name"]), stringFromAny(payload["tool"]), stringFromAny(payload["action"]), compactJSONText(payload["arguments"])), max)
	case "todo.list":
		return truncateText(firstNonEmpty(messageTextFromAny(payload["items"]), compactJSONText(payload["items"])), max)
	case "error":
		return truncateText(firstNonEmpty(stringFromAny(payload["message"]), messageTextFromAny(payload["error"]), compactJSONText(payload["error"])), max)
	default:
		return truncateText(firstNonEmpty(stringFromAny(payload["text"]), messageTextFromAny(payload["summary"]), stringFromAny(payload["status"]), compactJSONText(payload)), max)
	}
}

func messageTextFromAny(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return stringFromAny(value)
	case []string:
		return strings.TrimSpace(strings.Join(typed, "\n"))
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := messageTextFromAny(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	case map[string]any:
		for _, key := range []string{"text", "content", "summary", "output", "result", "error", "command", "path", "kind"} {
			if text := messageTextFromAny(typed[key]); text != "" {
				return text
			}
		}
	}
	return ""
}

func compactJSONText(value any) string {
	if value == nil {
		return ""
	}
	raw, err := json.Marshal(value)
	if err != nil || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func stringFromAny(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func newJSONLScanner(r io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	return scanner
}

func messageText(content []struct {
	Type string `json:"type"`
	Text string `json:"text"`
}) string {
	var parts []string
	for _, item := range content {
		if item.Text != "" {
			parts = append(parts, item.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func sessionIDFromFilename(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for i := 0; i+36 <= len(base); i++ {
		candidate := base[i : i+36]
		if isUUIDLike(candidate) {
			return candidate
		}
	}
	return ""
}

func isUUIDLike(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
				return false
			}
		}
	}
	return true
}

func projectFromCWD(root, cwd string, updatedAt time.Time) Project {
	return Project{
		ID:           stableID(cwd),
		Name:         filepath.Base(cwd),
		Path:         cwd,
		Root:         root,
		LastActiveAt: updatedAt,
		HasAgentsMD:  fileExists(filepath.Join(cwd, "AGENTS.md")),
		GitBranch:    readGitBranch(cwd),
	}
}

func uniqueProjects(projects []Project) []Project {
	seen := map[string]Project{}
	for _, project := range projects {
		if existing, ok := seen[project.ID]; ok && existing.LastActiveAt.After(project.LastActiveAt) {
			continue
		}
		seen[project.ID] = project
	}
	out := make([]Project, 0, len(seen))
	for _, project := range seen {
		out = append(out, project)
	}
	return out
}

func uniqueSessions(sessions []Session) []Session {
	seen := map[string]Session{}
	for _, session := range sessions {
		seen[session.ID] = session
	}
	out := make([]Session, 0, len(seen))
	for _, session := range seen {
		out = append(out, session)
	}
	return out
}

func uniqueRuntimeSessions(sessions []RuntimeSession) []RuntimeSession {
	seen := map[string]RuntimeSession{}
	for _, session := range sessions {
		key := session.Runtime + "\x00" + session.SessionID
		if existing, ok := seen[key]; ok && existing.UpdatedAt.After(session.UpdatedAt) {
			continue
		}
		seen[key] = session
	}
	out := make([]RuntimeSession, 0, len(seen))
	for _, session := range seen {
		out = append(out, session)
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func readGitBranch(path string) string {
	data, err := os.ReadFile(filepath.Join(path, ".git", "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(data))
	if strings.HasPrefix(head, "ref: refs/heads/") {
		return strings.TrimPrefix(head, "ref: refs/heads/")
	}
	return ""
}

func stableID(value string) string {
	return bytesHash([]byte(strings.ToLower(filepath.Clean(value))))[:16]
}

func cleanExistingPath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

func truncateText(text string, max int) string {
	text = strings.TrimSpace(text)
	if max <= 0 || text == "" {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

func looksLikeContextBlock(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "<") || strings.HasPrefix(trimmed, "# AGENTS.md instructions") || strings.HasPrefix(trimmed, "# Codex desktop context") || strings.HasPrefix(trimmed, "The following is the Codex agent history")
}

func shouldSkipDir(root, path, name string, includeHidden bool) bool {
	lower := strings.ToLower(name)
	if lower == ".git" || lower == "node_modules" || lower == "vendor" || lower == "tmp" || lower == "temp" {
		return true
	}
	if !includeHidden && lower == ".codex" {
		parent := filepath.Dir(path)
		return filepath.Clean(parent) != filepath.Clean(root)
	}
	if !includeHidden && strings.HasPrefix(name, ".") {
		return true
	}
	return false
}

func fileHash(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

func bytesHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func joinEnv(key string, elems ...string) string {
	base := os.Getenv(key)
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elems...)...)
}

func firstEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func uniqueClean(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		if path == "" {
			continue
		}
		cleaned := filepath.Clean(path)
		if abs, err := filepath.Abs(cleaned); err == nil {
			cleaned = abs
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	return out
}
