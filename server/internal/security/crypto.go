package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
)

type EncryptedPayload struct {
	ServerKeyID      string `json:"server_key_id"`
	Algorithm        string `json:"algorithm"`
	KeyAlgorithm     string `json:"key_algorithm"`
	ContentAlgorithm string `json:"content_algorithm"`
	EncryptedKey     string `json:"encrypted_key"`
	Nonce            string `json:"nonce"`
	Ciphertext       string `json:"ciphertext"`
}

func DecryptFromAgent(privateKeyPEM string, payload EncryptedPayload) ([]byte, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	encryptedKey, err := base64.StdEncoding.DecodeString(payload.EncryptedKey)
	if err != nil {
		return nil, err
	}
	nonce, err := base64.StdEncoding.DecodeString(payload.Nonce)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(payload.Ciphertext)
	if err != nil {
		return nil, err
	}
	label := []byte(payload.ServerKeyID)
	contentKey, err := rsa.DecryptOAEP(sha256.New(), nil, key, encryptedKey, label)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(contentKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return aead.Open(nil, nonce, ciphertext, label)
}

func parsePrivateKey(privateKeyPEM string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, errors.New("server private key pem is invalid")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("server private key must be rsa")
	}
	return key, nil
}
