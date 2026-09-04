package contract_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestDiagnosticSecretsAreRedacted(t *testing.T) {
	for _, raw := range []string{`{"nested":[{"token":"SYNTHETIC secret with spaces"}]}`, `error: "password": "SYNTHETIC\" secret"`, `api_key=SYNTHETIC`, `{"api_key":{"nested":"SYNTHETIC"}}`} {
		got := contract.RedactRaw(raw)
		if strings.Contains(got, "SYNTHETIC") || strings.Contains(got, "secret with spaces") {
			t.Fatalf("secret survived: %s", got)
		}
	}
}
