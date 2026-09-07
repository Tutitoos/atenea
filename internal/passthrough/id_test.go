package passthrough

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestPlainJSONReplyMustMatchRequestID(t *testing.T) {
	if _, err := decode(`{"jsonrpc":"2.0","id":8,"result":{}}`, 7); err == nil {
		t.Fatal("plain response with mismatched id was accepted")
	}
	if _, err := decode(`{"jsonrpc":"2.0","id":7,"result":{}}`, 7); err != nil {
		t.Fatalf("matching plain response rejected: %v", err)
	}
}

func TestErrorReplyMayCarryNullID(t *testing.T) {
	raw, err := decode(`{"jsonrpc":"2.0","id":null,"error":{"code":-32603,"message":"backend failed"}}`, 7)
	if err != nil {
		t.Fatalf("null-id error rejected before its message could be read: %v", err)
	}
	if _, err := resultOf(raw, "tools/call", func(_ contract.FailureKind, format string, args ...any) error {
		return fmt.Errorf(format, args...)
	}); err == nil || !strings.Contains(err.Error(), "backend failed") {
		t.Fatalf("resultOf error = %v, want remote message", err)
	}
}
