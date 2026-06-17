//go:build !windows

package securestore

import "os"

func readPrivateFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func writePrivateFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
