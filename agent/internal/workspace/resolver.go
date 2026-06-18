package workspace

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	goruntime "runtime"
	"strings"
)

type Target struct {
	Root string
	Path string
	Rel  string
}

type Resolver struct {
	state State
}

func NewResolver(state State) Resolver {
	return Resolver{state: state}
}

func (r Resolver) Resolve(ctx context.Context, projectID, rel string) (Target, error) {
	rel, err := cleanRel(rel)
	if err != nil {
		return Target{Rel: rel}, err
	}
	if r.state == nil {
		return Target{Rel: rel}, errors.New("workspace state is unavailable")
	}
	if projectID == "" {
		return Target{Rel: rel}, errors.New("project_id is required")
	}
	root, err := r.state.ProjectPath(ctx, projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return Target{Rel: rel}, errors.New("project is not indexed locally")
	}
	if err != nil {
		return Target{Rel: rel}, err
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return Target{Rel: rel}, errors.New("project path is empty")
	}
	realRoot, err := realDirectory(root)
	if err != nil {
		return Target{Rel: rel}, err
	}
	target := filepath.Join(realRoot, filepath.FromSlash(rel))
	target = filepath.Clean(target)
	realTarget, err := realPath(target)
	if err != nil {
		return Target{Rel: rel}, err
	}
	if !pathWithin(realRoot, realTarget) {
		return Target{Rel: rel}, errors.New("path escapes project root")
	}
	return Target{Root: realRoot, Path: realTarget, Rel: rel}, nil
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
	info, err := filesystemStat(path)
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
