package store

import (
	"strings"
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

func TestSaveAgentRegisterDoesNotOverwriteBoundCredential(t *testing.T) {
	query := strings.ToLower(saveAgentRegisterSQL())
	if strings.Contains(query, "credential") {
		t.Fatalf("register update must not overwrite bound credential: %s", query)
	}
	for _, want := range []string{"protocol_version", "capabilities", "registered_at", "last_seen_at"} {
		if !strings.Contains(query, want) {
			t.Fatalf("register update missing %q: %s", want, query)
		}
	}
}

func TestRuntimeSessionUpsertIsAgentScoped(t *testing.T) {
	query := strings.ToLower(upsertRuntimeSessionSQL())
	if !strings.Contains(query, "on conflict (agent_id, id)") {
		t.Fatalf("runtime session upsert must be agent scoped: %s", query)
	}
	if strings.Contains(query, "on conflict (id)") {
		t.Fatalf("runtime session upsert must not use global id conflict: %s", query)
	}
}
