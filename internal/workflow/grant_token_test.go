package workflow

import (
	"encoding/hex"
	"testing"
)

func TestFreshGrantTokenSatisfiesStrongGrantTokenShape(t *testing.T) {
	token, err := freshGrantToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) < 64 || len(token)%2 != 0 {
		t.Fatalf("token length = %d, want an even length of at least 64 hex characters", len(token))
	}
	decoded, err := hex.DecodeString(token)
	if err != nil {
		t.Fatalf("token is not hexadecimal: %v", err)
	}
	if len(decoded) < 32 {
		t.Fatalf("decoded token length = %d, want at least 32 bytes", len(decoded))
	}
}
