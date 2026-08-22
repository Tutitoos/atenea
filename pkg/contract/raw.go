package contract

import (
	"regexp"
	"strings"
)

// MaxPersistedRaw bounds provider text retained in durable stores. Provider
// output is diagnostic evidence, not an unbounded log.
const MaxPersistedRaw = 64 << 10

var (
	privateKeyRaw = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	bearerRaw     = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	secretRaw     = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd|secret|token)\b\s*[:=]\s*["']?)[^ \t\r\n"',;]+`)
	queryRaw      = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|refresh[_-]?token|secret|token|password)=)[^&\s]+`)
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
	if !strings.ContainsAny(text, "=:?&") &&
		!strings.Contains(text, "Bearer") &&
		!strings.Contains(text, "bearer") &&
		!strings.Contains(text, "BEARER") &&
		!strings.Contains(text, "-----BEGIN") {
		return boundRaw(text)
	}
	text = privateKeyRaw.ReplaceAllString(text, "[REDACTED PRIVATE KEY]")
	text = bearerRaw.ReplaceAllString(text, "Bearer [REDACTED]")
	text = secretRaw.ReplaceAllString(text, "${1}[REDACTED]")
	text = queryRaw.ReplaceAllString(text, "${1}[REDACTED]")
	return boundRaw(text)
}

func boundRaw(text string) string {
	if len(text) > MaxPersistedRaw {
		text = text[:MaxPersistedRaw] + "\n[TRUNCATED]"
	}
	return text
}
