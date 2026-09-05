package core

import (
	"context"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestMCPObservationPreservesFailureMeaning(t *testing.T) {
	for _, tc := range []struct {
		name, code, outcome, verdict string
		err                          error
	}{
		{"functional", "INVALID_ARGS", "fail", "failed", nil},
		{"refusal", "permission_denied", "refused", "refused", nil},
		{"cancel", "canceled", "cancel", "canceled", context.Canceled},
		{"timeout", "timeout", "fail", "failed", context.DeadlineExceeded},
		{"transport", "unavailable", "fail", "failed", contract.Fail(contract.FailureUnavailable, "offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{"isError": true, "structuredContent": map[string]any{"error": map[string]any{"code": tc.code, "message": "diagnosis"}}}
			o := observeMCP(body, nil, tc.err)
			if o.Outcome != tc.outcome || o.Code != tc.code || o.verdict() != tc.verdict {
				t.Fatalf("observation=%+v verdict=%s", o, o.verdict())
			}
			if o.compatibility(false) == "available" {
				t.Fatal("error became success")
			}
		})
	}
	if o := observeMCP(map[string]any{"isError": false}, nil, nil); o.Outcome != "ok" || o.Code != "" {
		t.Fatal(o)
	}
	if o := observeMCP(nil, &rpcError{Code: codeInternal, Message: "malformed backend"}, nil); o.Code != "invalid_response" {
		t.Fatal(o)
	}
}

func TestMCPObservationSanitizesEarlyErrors(t *testing.T) {
	for _, observation := range []mcpObservation{
		observeMCP(nil, nil, contract.Fail(contract.FailureUnavailable, "Authorization: Bearer secret-token")),
		observeMCP(nil, &rpcError{Code: codeInternal, Message: "Authorization: Bearer secret-token"}, nil),
	} {
		if strings.Contains(observation.Reason, "secret-token") || !strings.Contains(observation.Reason, "[REDACTED]") {
			t.Fatalf("unsafe observation reason: %q", observation.Reason)
		}
	}
}

func TestMCPObservationReadsTextOnlyProviderError(t *testing.T) {
	o := observeMCP(map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": `{"error":{"code":"SESSION_NOT_FOUND","message":"No active session"}}`}}}, nil, nil)
	if o.Code != "SESSION_NOT_FOUND" || o.Reason != "No active session" {
		t.Fatal(o)
	}
}

func TestDeviceSessionUnionIsNeverExposedByProfiles(t *testing.T) {
	for _, policy := range []desktopPolicy{{}, {Profile: "agent-device-full", RawCatalogs: map[string]string{"agent-device": "full"}}, {Profile: "chatgpt", RawCatalogs: map[string]string{"agent-device": "core"}}} {
		if policy.allows("raw.agent-device.session") {
			t.Fatal("session actions escaped the list-only command", policy)
		}
	}
}
