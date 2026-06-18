package workspace

import (
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
	defaultListDepth    = 1
	maxListDepth        = 8
)

type Filesystem struct {
	Policy Policy
}

func (fs Filesystem) List(target Target, req protocol.WorkspaceRequest) ([]protocol.WorkspaceEntry, error) {
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
		if !req.IncludeHidden && policy.Hidden(name) {
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
		return protocol.WorkspaceFile{Path: rel, Size: info.Size(), ModTime: info.ModTime().UTC(), Encoding: "utf-8", Sensitive: true}, errors.New("refusing to read sensitive file")
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
