package core

import (
	"encoding/json"
	"strings"

	"github.com/Tutitoos/atenea/internal/toolstats"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type observationKey struct{}

// mcpObservation is computed once at the execution boundary, before receipts.
// It carries diagnostics, never the provider's response or request payload.
type mcpObservation struct {
	Outcome, Code, Reason, ReceiptID string
	ProviderVersion, SchemaHash      string
	Ready                            bool
}

func observeMCP(result any, rpcErr *rpcError, err error) (o mcpObservation) {
	o = mcpObservation{Outcome: "ok", Ready: true}
	defer func() {
		o.Reason = toolstats.Clean(contract.RedactRaw(o.Reason), 240)
		o.Code = toolstats.Clean(o.Code, 80)
	}()
	if err != nil {
		o.Outcome, o.Code, o.Reason = toolstats.Outcome(err)
		return o
	}
	if rpcErr != nil {
		o.Outcome, o.Code, o.Reason = "fail", "invalid_request", rpcErr.Message
		if rpcErr.Code == codeInternal {
			o.Code = "invalid_response"
		}
		return o
	}
	m, _ := result.(map[string]any)
	if failed, _ := m["isError"].(bool); !failed {
		return o
	}
	o.Outcome, o.Code, o.Reason = "fail", "tool_failure", "Tool returned isError"
	if items, ok := m["content"].([]any); ok {
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if text, ok := item["text"].(string); ok && text != "" {
				o.Reason = text
				var body map[string]any
				if json.Unmarshal([]byte(text), &body) == nil {
					o.readDiagnostic(body)
				}
				break
			}
		}
	}
	if body, ok := m["structuredContent"].(map[string]any); ok {
		o.readDiagnostic(body)
	}
	switch strings.ToLower(o.Code) {
	case "profile_denied", "permission_denied", "external_denied":
		o.Outcome = "refused"
	case "canceled", "cancelled": //nolint:misspell // Accept both provider protocol spellings.
		o.Outcome = "cancel"
	}
	return o
}

func (o *mcpObservation) readDiagnostic(body map[string]any) {
	if nested, ok := body["error"].(map[string]any); ok {
		o.readDiagnostic(nested)
	}
	for _, key := range []string{"code", "error_code"} {
		if code, ok := body[key].(string); ok && code != "" {
			o.Code = code
		}
	}
	if message, ok := body["message"].(string); ok && message != "" {
		o.Reason = message
	}
}

func (o mcpObservation) verdict() string {
	switch o.Outcome {
	case "ok":
		return contract.VerdictOK.String()
	case "cancel":
		return contract.VerdictCanceled.String()
	case "refused":
		return contract.VerdictRefused.String()
	default:
		return contract.VerdictFailed.String()
	}
}

func (o mcpObservation) compatibility(fallback bool) string {
	switch o.Outcome {
	case "ok":
		return "available"
	case "refused":
		return "denied"
	case "cancel":
		return "canceled"
	default:
		if fallback {
			return "fallback"
		}
		return "error"
	}
}
