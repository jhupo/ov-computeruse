package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ov-computeruse/agent/internal/protocol"
)

const (
	defaultListLimit    = 500
	maxListLimit        = 2000
	defaultReadMaxBytes = 256 << 10
	maxReadMaxBytes     = 2 << 20
	searchContentBytes  = 128 << 10
	defaultListDepth    = 1
	maxListDepth        = 8
)

type Filesystem struct {
	Policy Policy
}

func (fs Filesystem) List(ctx context.Context, target Target, req protocol.WorkspaceRequest) ([]protocol.WorkspaceEntry, error) {
	policy := fs.policy()
	info, err := os.Stat(target.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("path is not a directory")
	}
	limit := clamp(req.Limit, defaultListLimit, maxListLimit)
	depth := clamp(req.Depth, defaultListDepth, maxListDepth)
	entries := make([]protocol.WorkspaceEntry, 0)
	err = filepath.WalkDir(target.Path, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		if samePath(path, target.Path) {
			return nil
		}
		relToTarget, err := filepath.Rel(target.Path, path)
		if err != nil {
			return nil
		}
		level := pathDepth(relToTarget)
		if level > depth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if entry.IsDir() && policy.SkipDir(name) {
			return filepath.SkipDir
		}
		if !req.IncludeHidden && policy.Hidden(name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if policy.Sensitive(path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(target.Root, path)
		if err != nil {
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		entries = append(entries, protocol.WorkspaceEntry{
			Name:      name,
			Path:      filepath.ToSlash(rel),
			Kind:      kind,
			Size:      info.Size(),
			ModTime:   info.ModTime().UTC(),
			Sensitive: policy.Sensitive(path),
		})
		if len(entries) >= limit {
			return errLimitReached
		}
		return nil
	})
	if errors.Is(err, errLimitReached) {
		err = nil
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind == "directory"
		}
		return strings.ToLower(entries[i].Path) < strings.ToLower(entries[j].Path)
	})
	return entries, err
}

func (fs Filesystem) Search(ctx context.Context, target Target, req protocol.WorkspaceRequest) ([]protocol.WorkspaceSearchMatch, error) {
	policy := fs.policy()
	query := strings.ToLower(strings.TrimSpace(req.Query))
	if query == "" {
		return nil, errors.New("query is required")
	}
	info, err := os.Stat(target.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("path is not a directory")
	}
	limit := clamp(req.Limit, 100, maxListLimit)
	depth := clamp(req.Depth, maxListDepth, maxListDepth)
	matches := make([]protocol.WorkspaceSearchMatch, 0)
	err = filepath.WalkDir(target.Path, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		if samePath(path, target.Path) {
			return nil
		}
		relToTarget, err := filepath.Rel(target.Path, path)
		if err != nil {
			return nil
		}
		level := pathDepth(relToTarget)
		if level > depth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if entry.IsDir() && policy.SkipDir(name) {
			return filepath.SkipDir
		}
		if !req.IncludeHidden && policy.Hidden(name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if policy.Sensitive(path) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(target.Root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		kind := "file"
		if entry.IsDir() {
			kind = "directory"
		}
		score := searchScore(query, strings.ToLower(name), strings.ToLower(rel))
		match := protocol.WorkspaceSearchMatch{
			Path:      rel,
			Name:      name,
			Kind:      kind,
			Score:     score,
			Size:      info.Size(),
			ModTime:   info.ModTime().UTC(),
			Sensitive: policy.Sensitive(path),
		}
		if score == 0 && kind == "file" && !match.Sensitive {
			line, preview, contentScore := fs.contentMatch(ctx, path, query, info.Size())
			match.Line = line
			match.Preview = preview
			match.Score = contentScore
		}
		if match.Score == 0 {
			return nil
		}
		matches = append(matches, match)
		if len(matches) >= limit {
			return errLimitReached
		}
		return nil
	})
	if errors.Is(err, errLimitReached) {
		err = nil
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		if matches[i].Kind != matches[j].Kind {
			return matches[i].Kind == "file"
		}
		return strings.ToLower(matches[i].Path) < strings.ToLower(matches[j].Path)
	})
	return matches, err
}

func (fs Filesystem) contentMatch(ctx context.Context, path string, query string, size int64) (int, string, int) {
	if size <= 0 || size > searchContentBytes {
		return 0, "", 0
	}
	if ctx.Err() != nil {
		return 0, "", 0
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, "", 0
	}
	defer file.Close()
	if ctx.Err() != nil {
		return 0, "", 0
	}
	data, err := io.ReadAll(io.LimitReader(file, searchContentBytes+1))
	if err != nil || int64(len(data)) > searchContentBytes || fs.policy().Binary(data) {
		return 0, "", 0
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(strings.ToLower(trimmed), query) {
			return i + 1, truncatePreview(trimmed, 180), 30
		}
	}
	return 0, "", 0
}

func (fs Filesystem) Read(target Target, req protocol.WorkspaceRequest) (protocol.WorkspaceFile, error) {
	policy := fs.policy()
	info, err := os.Stat(target.Path)
	if err != nil {
		return protocol.WorkspaceFile{}, err
	}
	if info.IsDir() {
		return protocol.WorkspaceFile{}, errors.New("path is a directory")
	}
	rel, err := filepath.Rel(target.Root, target.Path)
	if err != nil {
		return protocol.WorkspaceFile{}, err
	}
	rel = filepath.ToSlash(rel)
	if policy.Sensitive(target.Path) {
		return protocol.WorkspaceFile{Path: rel, Size: info.Size(), ModTime: info.ModTime().UTC(), Encoding: "utf-8", Sensitive: true}, workspaceErr("permission_denied", "refusing to read sensitive file")
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadMaxBytes
	}
	if maxBytes > maxReadMaxBytes {
		maxBytes = maxReadMaxBytes
	}
	file, err := os.Open(target.Path)
	if err != nil {
		return protocol.WorkspaceFile{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return protocol.WorkspaceFile{}, err
	}
	truncated := int64(len(data)) > maxBytes
	if truncated {
		data = data[:maxBytes]
	}
	if policy.Binary(data) {
		return protocol.WorkspaceFile{Path: rel, Size: info.Size(), ModTime: info.ModTime().UTC(), Encoding: "binary", Binary: true}, nil
	}
	sum := sha256.Sum256(data)
	return protocol.WorkspaceFile{
		Path:      rel,
		Size:      info.Size(),
		ModTime:   info.ModTime().UTC(),
		SHA256:    hex.EncodeToString(sum[:]),
		Encoding:  "utf-8",
		Content:   string(data),
		Truncated: truncated,
	}, nil
}

func searchScore(query, name, rel string) int {
	switch {
	case name == query:
		return 100
	case strings.HasPrefix(name, query):
		return 80
	case strings.Contains(name, query):
		return 60
	case rel == query:
		return 50
	case strings.Contains(rel, query):
		return 40
	default:
		return 0
	}
}

func truncatePreview(text string, max int) string {
	if max <= 0 || len([]rune(text)) <= max {
		return text
	}
	runes := []rune(text)
	return string(runes[:max])
}

func (fs Filesystem) policy() Policy {
	if fs.Policy == nil {
		return DefaultPolicy{}
	}
	return fs.Policy
}

func pathDepth(rel string) int {
	if rel == "." || rel == "" {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

func clamp(value, fallback, max int) int {
	if value <= 0 {
		return fallback
	}
	if value > max {
		return max
	}
	return value
}

func filesystemStat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

var errLimitReached = errors.New("workspace list limit reached")
