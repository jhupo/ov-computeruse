package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
)

func Sign(secret string, data []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(data)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func Verify(secret string, data []byte, signature string) bool {
	expected := Sign(secret, data)
	return hmac.Equal([]byte(expected), []byte(signature))
}
