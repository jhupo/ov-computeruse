package codexscan

import (
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
	Roots    []Root    `json:"roots"`
	Files    []File    `json:"files"`
	Projects []Project `json:"projects"`
	Sessions []Session `json:"sessions"`
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
	ProjectID     string    `json:"project_id,omitempty"`
	Title         string    `json:"title,omitempty"`
	Path          string    `json:"path"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
	Size          int64     `json:"size,omitempty"`
	ContentSHA256 string    `json:"content_sha256,omitempty"`
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
				result.Sessions = append(result.Sessions, sessionFromFile(path, info, maxBytes))
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
	case strings.HasPrefix(relSlash, "sessions/"):
		return "session"
	case strings.Contains(relSlash, "/sessions/"):
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

func sessionFromFile(path string, info os.FileInfo, maxBytes int64) Session {
	contentHash := ""
	if maxBytes <= 0 || info.Size() <= maxBytes {
		contentHash = fileHash(path)
	}
	return Session{
		ID:            stableID(path),
		Title:         strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Path:          path,
		UpdatedAt:     info.ModTime().UTC(),
		Size:          info.Size(),
		ContentSHA256: contentHash,
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
