package contract

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxPersistedRaw bounds provider text retained in durable stores. Provider
// output is diagnostic evidence, not an unbounded log.
const MaxPersistedRaw = 64 << 10

var (
	quotedSecretRaw = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd|secret|token)\b["']?\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*')`)
	privateKeyRaw   = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	bearerRaw       = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	secretRaw       = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd|secret|token)\b["']?\s*[:=]\s*["']?)[^ \t\r\n"',;]+`)
	queryRaw        = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|secret|token|password)=)[^&\s]+`)
)

// RedactRaw prepares provider output for durable storage. The in-memory
// Failure.Raw remains untouched so adapters and callers can still inspect the
// original response during the active commission.
func RedactRaw(raw string) string {
	text := strings.TrimSpace(raw)
	if text == "" {
		return ""
	}
	// Most successful provider diagnostics contain no credential-bearing
	// delimiter at all. Avoid four regular-expression passes on that hot path;
	// the bounded return still applies to ordinary oversized output.
	//
	// A shortcut past a redaction is only safe while it is strictly wider than
	// what it skips. The bearer test lowercases once instead of comparing
	// three spellings, because bearerRaw is compiled (?i): `BeArEr <token>` is
	// a match the regular expression would have removed and the three literal
	// comparisons let through, so the shortcut was narrower than the pass it
	// replaced and any capitalization outside those three reached disk intact.
	// ContainsAny runs first and is false for nearly all real credentials, so
	// the extra allocation is not on the path this shortcut exists to protect.
	if !strings.ContainsAny(text, "=:?&") &&
		!strings.Contains(strings.ToLower(text), "bearer") &&
		!strings.Contains(text, "-----BEGIN") {
		return boundRaw(text)
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil && decoder.Decode(new(any)) == io.EOF {
		redactJSON(value)
		if encoded, err := json.Marshal(value); err == nil {
			text = string(encoded)
		}
	}
	text = quotedSecretRaw.ReplaceAllString(text, `${1}"[REDACTED]"`)
	text = privateKeyRaw.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
	text = bearerRaw.ReplaceAllString(text, "Bearer [REDACTED]")
	text = secretRaw.ReplaceAllString(text, "${1}[REDACTED]")
	text = queryRaw.ReplaceAllString(text, "${1}[REDACTED]")
	return boundRaw(text)
}

// boundRaw caps the text at MaxPersistedRaw bytes, cutting on a character
// boundary.
//
// The ceiling counts bytes because what it protects is store size, but the cut
// may not land inside a rune: slicing at a byte index splits whatever
// multi-byte character straddles it, and what reaches the store is then a
// replacement character in place of one the provider really sent -- invalid
// text manufactured out of valid input. That is the exact failure
// MaxDiscoveryLength is documented as counting runes to avoid, and provider
// diagnostics are where non-ASCII text actually turns up: a path, a filename,
// or a message in the operator's own language.
//
// A rune is at most utf8.UTFMax bytes, so stepping back off the continuation
// bytes is a bounded walk rather than a scan. The bound also means text that
// was not valid UTF-8 to begin with is truncated near the ceiling instead of
// being unwound to nothing.
func redactJSON(value any) {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "_", ""), "-", ""))
			switch normalized {
			case "apikey", "accesstoken", "refreshtoken", "clientsecret", "password", "passwd", "secret", "token":
				v[key] = "[REDACTED]"
			default:
				redactJSON(child)
			}
		}
	case []any:
		for _, child := range v {
			redactJSON(child)
		}
	}
}

func boundRaw(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	if len(text) <= MaxPersistedRaw {
		return text
	}
	cut := MaxPersistedRaw
	for range utf8.UTFMax - 1 {
		if utf8.RuneStart(text[cut]) {
			break
		}
		cut--
	}
	return text[:cut] + "\n[TRUNCATED]"
}
