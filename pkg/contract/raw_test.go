package contract_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestRedactRawRemovesCommonCredentials(t *testing.T) {
	input := `Authorization: Bearer abc.def
api_key="key-value" token: "token-value"
https://example.test/?access_token=query-value
-----BEGIN PRIVATE KEY-----
secret material
-----END PRIVATE KEY-----`

	got := contract.RedactRaw(input)
	for _, secret := range []string{"abc.def", "key-value", "token-value", "query-value", "secret material"} {
		if strings.Contains(got, secret) {
			t.Errorf("redacted output contains %q: %q", secret, got)
		}
	}
	for _, marker := range []string{"Bearer [REDACTED]", "[REDACTED PRIVATE KEY]"} {
		if !strings.Contains(got, marker) {
			t.Errorf("redacted output lacks %q: %q", marker, got)
		}
	}
}

func TestRedactRawBoundsDiagnosticOutput(t *testing.T) {
	got := contract.RedactRaw(strings.Repeat("x", contract.MaxPersistedRaw+100))
	if len(got) <= contract.MaxPersistedRaw || len(got) > contract.MaxPersistedRaw+len("\n[TRUNCATED]") {
		t.Fatalf("length = %d, want bounded output", len(got))
	}
	if !strings.HasSuffix(got, "\n[TRUNCATED]") {
		t.Fatalf("output is not marked truncated: %q", got[len(got)-20:])
	}
}
