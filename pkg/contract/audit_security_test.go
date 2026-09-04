package contract_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// TestDiagnosticSecretsAreRedacted covers nested and quoted credential diagnostics.
func TestDiagnosticSecretsAreRedacted(t *testing.T) {
	for _, raw := range []string{`{"nested":[{"token":"SYNTHETIC secret with spaces"}]}`, `error: "password": "SYNTHETIC\" secret"`, `api_key=SYNTHETIC`, `{"api_key":{"nested":"SYNTHETIC"}}`} {
		got := contract.RedactRaw(raw)
		if strings.Contains(got, "SYNTHETIC") || strings.Contains(got, "secret with spaces") {
			t.Fatalf("secret survived: %s", got)
		}
	}
}

// TestRedactionPreservesNumbersAndTrailingData guards identifier precision and complete-input parsing.
func TestRedactionPreservesNumbersAndTrailingData(t *testing.T) {
	for _, raw := range []string{`{"id":9007199254740993,"token":"SYNTHETIC"}`, `{"id":9007199254740993} trailing-marker`} {
		got := contract.RedactRaw(raw)
		if !strings.Contains(got, "9007199254740993") || strings.Contains(got, "SYNTHETIC") {
			t.Fatal(got)
		}
		if strings.Contains(raw, "trailing-marker") && !strings.Contains(got, "trailing-marker") {
			t.Fatal("trailing data discarded", got)
		}
	}
}

// TestRedactStructuredPrefixes covers complete documents and malformed suffixes.
func TestRedactStructuredPrefixes(t *testing.T) {
	for _, input := range []string{
		`{"outer":{"token":{"value":"SYNTHETIC"}}} trailing-marker`,
		`{"token":["SYNTHETIC"]} trailing-marker`,
		`{"id":9007199254740993} {"token":{"value":"SYNTHETIC"}} trailing-marker`,
		`{"ok":true} trailing-marker {"token":["SYNTHETIC"`,
		`{"ok":true} trailing-marker {"token":"SYNTHETIC secret with spaces`,
	} {
		out := contract.RedactRaw(input)
		if strings.Contains(out, "SYNTHETIC") || !strings.Contains(out, "trailing-marker") {
			t.Fatalf("unsafe or missing suffix: %s", out)
		}
		if strings.Contains(input, "9007199254740993") && !strings.Contains(out, "9007199254740993") {
			t.Fatal(out)
		}
	}
}
