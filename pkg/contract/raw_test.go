package contract_test

import (
	"strings"
	"testing"
	"unicode/utf8"

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

// bearerRaw is compiled (?i), so the shortcut that decides whether to run it
// has to be at least as wide. It tested three spellings -- Bearer, bearer,
// BEARER -- and every other capitalization walked past redaction entirely and
// reached the store with the token intact.
func TestRedactRawIgnoresTheCaseOfBearer(t *testing.T) {
	for _, spelling := range []string{"Bearer", "bearer", "BEARER", "BeArEr", "bEARER", "BEarer"} {
		// No `=`, `:`, `?` or `&` anywhere: those are the delimiters the
		// other half of the shortcut looks for, and with one present the
		// bearer test is never reached and the case would prove nothing.
		input := "request failed, sent " + spelling + " sk-live-abcdef123456 upstream"
		got := contract.RedactRaw(input)
		if strings.Contains(got, "sk-live-abcdef123456") {
			t.Errorf("%s: the token survived redaction: %q", spelling, got)
		}
		if !strings.Contains(got, "[REDACTED]") {
			t.Errorf("%s: nothing was redacted: %q", spelling, got)
		}
	}
}

// MaxPersistedRaw counts bytes because what it protects is store size, but the
// cut may not land inside a character. Slicing at a byte index splits whatever
// multi-byte rune straddles it, so what reaches the store is a replacement
// character in place of one the provider really sent -- invalid text
// manufactured out of valid input, which is the exact failure
// MaxDiscoveryLength is documented as counting runes to avoid.
func TestRawTruncationCutsOnACharacterBoundary(t *testing.T) {
	// "é" is two bytes, so a prefix of odd byte length lands mid-character.
	// Repeating it past the ceiling puts the cut somewhere inside one.
	oversized := strings.Repeat("é", contract.MaxPersistedRaw)
	got := contract.RedactRaw(oversized)

	if !strings.HasSuffix(got, "\n[TRUNCATED]") {
		t.Fatalf("output is not marked truncated: %q", got[len(got)-20:])
	}
	body := strings.TrimSuffix(got, "\n[TRUNCATED]")
	if !utf8.ValidString(body) {
		t.Fatal("truncation produced invalid UTF-8 from valid input")
	}
	if strings.ContainsRune(body, utf8.RuneError) {
		t.Fatal("truncation left a replacement character where the provider sent a real one")
	}
	if len(body) > contract.MaxPersistedRaw {
		t.Fatalf("body is %d bytes, above the ceiling of %d", len(body), contract.MaxPersistedRaw)
	}
	// The cut backs up by at most the width of one rune, so nothing beyond a
	// character's worth of evidence is lost to keep it valid.
	if contract.MaxPersistedRaw-len(body) >= utf8.UTFMax {
		t.Fatalf("body is %d bytes, %d short of the ceiling: the cut lost more than one character",
			len(body), contract.MaxPersistedRaw-len(body))
	}
}
