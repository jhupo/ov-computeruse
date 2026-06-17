package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
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

func EncryptForServer(serverKeyID, publicKeyPEM string, plaintext []byte) (EncryptedPayload, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return EncryptedPayload{}, errors.New("server public key pem is invalid")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		if cert, certErr := x509.ParseCertificate(block.Bytes); certErr == nil {
			key = cert.PublicKey
		} else {
			return EncryptedPayload{}, err
		}
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return EncryptedPayload{}, errors.New("server public key must be rsa")
	}

	contentKey := make([]byte, 32)
	if _, err := rand.Read(contentKey); err != nil {
		return EncryptedPayload{}, err
	}
	defer zero(contentKey)
	blockCipher, err := aes.NewCipher(contentKey)
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

	label := []byte(serverKeyID)
	encryptedKey, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, rsaKey, contentKey, label)
	if err != nil {
		return EncryptedPayload{}, err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, label)
	return EncryptedPayload{
		ServerKeyID:      serverKeyID,
		Algorithm:        "RSA-OAEP-SHA256+A256GCM",
		KeyAlgorithm:     "RSA-OAEP-SHA256",
		ContentAlgorithm: "A256GCM",
		EncryptedKey:     base64.StdEncoding.EncodeToString(encryptedKey),
		Nonce:            base64.StdEncoding.EncodeToString(nonce),
		Ciphertext:       base64.StdEncoding.EncodeToString(ciphertext),
	}, nil
}

func zero(data []byte) {
	for i := range data {
		data[i] = 0
	}
}

func PublicKeyFingerprint(publicKeyPEM string) (string, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return "", errors.New("server public key pem is invalid")
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

func FingerprintSecret(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}
