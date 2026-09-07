package mcpcompat

import (
	"encoding/base64"
	"strings"
	"unicode"
)

// EncodeSentinelValue encodes a value for the MCP header/name extension when
// its literal form would be ambiguous or unsafe on an HTTP header.  The
// sentinel is deliberately shared by the name and x-mcp-header paths so a
// provider cannot make one transport interpret a value differently from
// another.  An ordinary ASCII space inside a value is safe; only leading or
// trailing whitespace is encoded.
func EncodeSentinelValue(value string) string {
	if !needsSentinel(value) {
		return value
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(value)) + "?="
}

func needsSentinel(value string) bool {
	if strings.HasPrefix(value, "=?base64?") && strings.HasSuffix(value, "?=") {
		return true
	}
	for i, r := range value {
		if r > 127 || unicode.IsControl(r) || (unicode.IsSpace(r) && (i == 0 || i+len(string(r)) == len(value))) {
			return true
		}
	}
	return false
}
