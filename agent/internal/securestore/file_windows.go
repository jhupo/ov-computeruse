//go:build windows

package securestore

import (
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const dpapiFilePrefix = "ov-agent-dpapi-v1:"

var (
	crypt32                = windows.NewLazySystemDLL("crypt32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func readPrivateFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(data))
	if !strings.HasPrefix(text, dpapiFilePrefix) {
		return data, nil
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(text, dpapiFilePrefix))
	if err != nil {
		return nil, err
	}
	return dpapiUnprotect(raw)
}

func writePrivateFile(path string, data []byte) error {
	protected, err := dpapiProtect(data)
	if err != nil {
		return err
	}
	payload := []byte(dpapiFilePrefix + base64.StdEncoding.EncodeToString(protected))
	return os.WriteFile(path, payload, 0o600)
}

func dpapiProtect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("cannot protect empty data")
	}
	in := dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
	var out dataBlob
	ret, _, err := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return blobBytes(out), nil
}

func dpapiUnprotect(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("cannot unprotect empty data")
	}
	in := dataBlob{cbData: uint32(len(data)), pbData: &data[0]}
	var out dataBlob
	ret, _, err := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&in)),
		0,
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&out)),
	)
	if ret == 0 {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return blobBytes(out), nil
}

func blobBytes(blob dataBlob) []byte {
	if blob.cbData == 0 || blob.pbData == nil {
		return nil
	}
	view := unsafe.Slice(blob.pbData, blob.cbData)
	return append([]byte(nil), view...)
}
