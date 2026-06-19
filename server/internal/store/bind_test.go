package store

import (
	"testing"

	"ov-computeruse/server/internal/security"
)

func TestBindCredentialUsesNormalizedBaseURLFingerprint(t *testing.T) {
	baseURL, err := normalizeBaseURL("HTTPS://Gateway.Example/v1/?ignored=true#fragment")
	if err != nil {
		t.Fatal(err)
	}
	record := agentCredentialRecord{
		BaseURLFingerprint: security.FingerprintSecret(baseURL),
		KeyFingerprint:     "key_fp",
	}
	if record.BaseURLFingerprint != security.FingerprintSecret("https://gateway.example/v1") {
		t.Fatalf("base url fingerprint = %q", record.BaseURLFingerprint)
	}
	if record.KeyFingerprint != "key_fp" {
		t.Fatalf("key fingerprint = %q", record.KeyFingerprint)
	}
}
