package protocol

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
)

const (
	EnvelopeEncryptionA256GCM       = "A256GCM"
	EnvelopeEncryptionKeyHMACSHA256 = "HMAC-SHA256"
)

type Encryption struct {
	Algorithm    string `json:"algorithm,omitempty"`
	KeyAlgorithm string `json:"key_algorithm,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
}

func EncryptEnvelopeData(secret string, env Envelope) (Envelope, error) {
	if len(env.Data) == 0 {
		return env, nil
	}
	aead, err := envelopeAEAD(secret)
	if err != nil {
		return Envelope{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Envelope{}, err
	}
	env.Encryption = Encryption{
		Algorithm:    EnvelopeEncryptionA256GCM,
		KeyAlgorithm: EnvelopeEncryptionKeyHMACSHA256,
		Nonce:        base64.RawURLEncoding.EncodeToString(nonce),
	}
	env.Data = aead.Seal(nil, nonce, env.Data, envelopeAAD(env))
	return env, nil
}

func DecryptEnvelopeData(secret string, env Envelope) (Envelope, error) {
	if env.Encryption.Algorithm != EnvelopeEncryptionA256GCM || env.Encryption.KeyAlgorithm != EnvelopeEncryptionKeyHMACSHA256 {
		return Envelope{}, errors.New("unsupported envelope encryption")
	}
	nonce, err := base64.RawURLEncoding.DecodeString(env.Encryption.Nonce)
	if err != nil {
		return Envelope{}, err
	}
	aead, err := envelopeAEAD(secret)
	if err != nil {
		return Envelope{}, err
	}
	plaintext, err := aead.Open(nil, nonce, env.Data, envelopeAAD(env))
	if err != nil {
		return Envelope{}, err
	}
	env.Data = plaintext
	return env, nil
}

func envelopeAEAD(secret string) (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte("ov-computeruse envelope a256gcm v1"))
	key := mac.Sum(nil)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func envelopeAAD(env Envelope) []byte {
	hash := sha256.New()
	_, _ = hash.Write([]byte(env.Version))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(env.MessageID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(env.Type))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(env.AgentID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(env.DeviceID))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(env.Nonce))
	return hash.Sum(nil)
}
