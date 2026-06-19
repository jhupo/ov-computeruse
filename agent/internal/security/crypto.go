package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
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

func EncryptForServer(token string, plaintext []byte) (EncryptedPayload, error) {
	key, err := tokenKey(token)
	if err != nil {
		return EncryptedPayload{}, err
	}
	blockCipher, err := aes.NewCipher(key)
	if err != nil {
		return EncryptedPayload{}, err
	}
	aead, err := cipher.NewGCM(blockCipher)
	if err != nil {
		return EncryptedPayload{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return EncryptedPayload{}, err
	}

	ciphertext := aead.Seal(nil, nonce, plaintext, bindAAD())
	return EncryptedPayload{
		Algorithm:        "OV-TOKEN+A256GCM",
		ContentAlgorithm: "A256GCM",
		Nonce:            base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:       base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
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
