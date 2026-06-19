package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
)

type EncryptedPayload struct {
	Algorithm        string `json:"algorithm"`
	ContentAlgorithm string `json:"content_algorithm"`
	Nonce            string `json:"nonce"`
	Ciphertext       string `json:"ciphertext"`
}

func DecryptFromAgent(token string, payload EncryptedPayload) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, err
	}
	key, err := tokenKey(token)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, bindAAD())
}

func tokenKey(token string) ([]byte, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("token is required")
	}
	hash := sha256.Sum256([]byte("ov-computeruse/token/v1\x00" + token))
	return hash[:], nil
}

func bindAAD() []byte {
	return []byte("ov-computeruse/agent-bind/v1")
}

func FingerprintSecret(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}
