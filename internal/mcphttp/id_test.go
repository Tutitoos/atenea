package mcphttp

import "testing"

func TestPlainJSONReplyMustMatchRequestID(t *testing.T) {
	if _, err := decode(answer{body: `{"jsonrpc":"2.0","id":8,"result":{}}`}, 7); err == nil {
		t.Fatal("plain response with mismatched id was accepted")
	}
	if _, err := decode(answer{body: `{"jsonrpc":"2.0","id":7,"result":{}}`}, 7); err != nil {
		t.Fatalf("matching plain response rejected: %v", err)
	}
}
