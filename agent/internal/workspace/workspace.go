package workspace

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"ov-computeruse/agent/internal/protocol"
)

const (
	defaultListLimit     = 500
	maxListLimit         = 2000
	defaultReadMaxBytes  = 256 << 10
	maxReadMaxBytes      = 2 << 20
	defaultListDepth     = 1
	maxListDepth         = 8
	workspaceFeatureName = "workspace.files"
)

type State interface {
	ProjectPath(context.Context, string) (string, error)
}

type Handler struct {
	state State
}

func New(state State) Handler {
	return Handler{state: state}
}

func FeatureName() string {
	return workspaceFeatureName
}

func (h Handler) Handle(ctx context.Context, req protocol.WorkspaceRequest) protocol.WorkspaceResponse {
	rel, err := cleanRel(req.Path)
	resp := protocol.WorkspaceResponse{
		RequestID: req.RequestID,
		Operation: strings.TrimSpace(req.Operation),
		ProjectID: strings.TrimSpace(req.ProjectID),
		Path:      rel,
		At:        time.Now().UTC(),
	}
	if err != nil {
		resp.Status = "rejected"
		resp.Message = err.Error()
		return resp
	}
	root, target, err := h.resolveTarget(ctx, resp.ProjectID, resp.Path)
	if err != nil {
		resp.Status = "rejected"
		resp.Message = err.Error()
		return resp
	}
	switch resp.Operation {
	case "list":
		entries, err := listEntries(root, target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.Entries = entries
	case "read":
		file, err := readFile(root, target, req)
		if err != nil {
			resp.Status = "failed"
			resp.Message = err.Error()
			return resp
		}
		resp.Status = "ok"
		resp.File = &file
	default:
		resp.Status = "rejected"
		resp.Message = "unsupported workspace operation"
	}
	return resp
}

func (h Handler) resolveTarget(ctx context.Context, projectID, rel string) (string, string, error) {
	if h.state == nil {
		return "", "", errors.New("workspace state is unavailable")
	}
	if projectID == "" {
		return "", "", errors.New("project_id is required")
	}
	root, err := h.state.ProjectPath(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errors.New("project is not indexed locally")
	}
	if err != nil {
		return "", "", err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", errors.New("project path is empty")
	}
	realRoot, err := realDirectory(root)
	if err != nil {
		return "", "", err
	}
	target := filepath.Join(realRoot, filepath.FromSlash(rel))
	target = filepath.Clean(target)
	realTarget, err := realPath(target)
	if err != nil {
		return "", "", err
	}
	if !pathWithin(realRoot, realTarget) {
		return "", "", errors.New("path escapes project root")
	}
	return realRoot, realTarget, nil
}

func listEntries(root, target string, req protocol.WorkspaceRequest) ([]protocol.WorkspaceEntry, error) {
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("path is not a directory")
	}
	limit := clamp(req.Limit, defaultListLimit, maxListLimit)
	depth := clamp(req.Depth, defaultListDepth, maxListDepth)
	entries := make([]protocol.WorkspaceEntry, 0)
	err = filepath.WalkDir(target, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if samePath(path, target) {
			return nil
		}
		relToTarget, err := filepath.Rel(target, path)
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
		sensitive := isSensitivePath(path)
		if !req.IncludeHidden && isHiddenName(name) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, path)
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
			Sensitive: sensitive,
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

func readFile(root, target string, req protocol.WorkspaceRequest) (protocol.WorkspaceFile, error) {
	info, err := os.Stat(target)
	if err != nil {
		return protocol.WorkspaceFile{}, err
	}
	if info.IsDir() {
		return protocol.WorkspaceFile{}, errors.New("path is a directory")
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return protocol.WorkspaceFile{}, err
	}
	rel = filepath.ToSlash(rel)
	sensitive := isSensitivePath(target)
	if sensitive {
		return protocol.WorkspaceFile{Path: rel, Size: info.Size(), ModTime: info.ModTime().UTC(), Encoding: "utf-8", Sensitive: true}, errors.New("refusing to read sensitive file")
	}
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = defaultReadMaxBytes
	}
	if maxBytes > maxReadMaxBytes {
		maxBytes = maxReadMaxBytes
	}
	file, err := os.Open(target)
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
	binary := isBinary(data)
	if binary {
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
		Sensitive: false,
	}, nil
}

func cleanRel(path string) (string, error) {
	path = strings.TrimSpace(filepath.ToSlash(path))
	path = strings.TrimPrefix(path, "/")
	path = filepath.Clean(filepath.FromSlash(path))
	if path == "." {
		return "", nil
	}
	if path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes project root")
	}
	return filepath.ToSlash(path), nil
}

func realDirectory(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("project path is not a directory")
	}
	return realPath(path)
}

func realPath(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}

func pathWithin(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if samePath(root, path) {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func samePath(left, right string) bool {
	if goruntime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func isHiddenName(name string) bool {
	return strings.HasPrefix(name, ".")
}

func isSensitivePath(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, ".env") || strings.Contains(base, "secret") || strings.Contains(base, "token") || strings.Contains(base, "credential") {
		return true
	}
	return strings.Contains(normalized, "/.git/") || strings.Contains(normalized, "/node_modules/") || strings.Contains(normalized, "/vendor/")
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if !utf8.Valid(data) {
		return true
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
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

var errLimitReached = errors.New("workspace list limit reached")
