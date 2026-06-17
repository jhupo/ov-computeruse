//go:build windows

package securestore

import "os"

func writePrivateFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}
