package workspace

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type Policy interface {
	Hidden(name string) bool
	Sensitive(path string) bool
	Binary(data []byte) bool
}

type DefaultPolicy struct{}

func (DefaultPolicy) Hidden(name string) bool {
	return strings.HasPrefix(name, ".")
}

func (DefaultPolicy) Sensitive(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, ".env") || strings.Contains(base, "secret") || strings.Contains(base, "token") || strings.Contains(base, "credential") {
		return true
	}
	return strings.Contains(normalized, "/.git/") || strings.Contains(normalized, "/node_modules/") || strings.Contains(normalized, "/vendor/")
}

func (DefaultPolicy) Binary(data []byte) bool {
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
