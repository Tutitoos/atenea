package mcpcompat

import (
	"encoding/json"
)

// LegacyHTTPFallback reports whether an HTTP status can safely trigger the
// legacy retry. Only compatibility statuses are eligible; authentication,
// server failures and transport failures never downgrade, regardless of the
// response body. For an eligible status, a recognizable JSON-RPC error is
// authoritative: only -32601 means the modern method is absent. Plain bodies
// use the status itself as the only signal; response text is ignored.
func LegacyHTTPFallback(status int, body string) bool {
	if status != 400 && status != 404 && status != 405 {
		return false
	}
	var envelope struct {
		JSONRPC string `json:"jsonrpc"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal([]byte(body), &envelope) == nil && envelope.JSONRPC == "2.0" && (envelope.Error != nil || len(envelope.Result) > 0) {
		return envelope.Error != nil && envelope.Error.Code == -32601
	}
	return true
}
