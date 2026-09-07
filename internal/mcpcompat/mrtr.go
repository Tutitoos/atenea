package mcpcompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ResultType is the result envelope type used by modern MCP responses.
// InputRequired is an observation: transports preserve its request state for
// the caller, but Atenea never executes a client request or invents answers.
type ResultType string

const (
	// ResultComplete is part of ATENEA's public orchestration contract.
	ResultComplete ResultType = "complete"
	// ResultInputRequired is part of ATENEA's public orchestration contract.
	ResultInputRequired ResultType = "input_required"
)

// ErrInvalidMRTR is part of ATENEA's public orchestration contract.
var ErrInvalidMRTR = errors.New("invalid MCP result type")

// ErrFutureResult is part of ATENEA's public orchestration contract.
var ErrFutureResult = errors.New("modern response has an unsupported result type")

// FutureResultError is an opaque, typed observation of a result extension
// this build does not execute yet. Response.Raw and Response.RequestState are
// retained verbatim so a later implementation can continue the protocol
// without asking the provider to repeat the operation. Callers must treat it
// as data, never as an instruction to auto-retry or answer.
type FutureResultError struct {
	Response MRTR
}

func (e *FutureResultError) Error() string {
	if e == nil {
		return ErrFutureResult.Error()
	}
	return fmt.Sprintf("%s: %s", ErrFutureResult, e.Response.Type)
}

func (e *FutureResultError) Unwrap() error { return ErrFutureResult }

// OpaqueResultError is a descriptive alias for callers that emphasize that
// the future payload is preserved rather than interpreted.
type OpaqueResultError = FutureResultError

// MRTR is the shared result contract. The input fields intentionally remain
// raw JSON: they are untrusted provider data and must not become executable
// instructions or be silently rewritten by a transport.
type MRTR struct {
	Type           ResultType
	InputRequests  json.RawMessage
	RequestState   json.RawMessage
	InputResponses json.RawMessage
	Raw            json.RawMessage
}

// ParseMRTR accepts the two result states defined by the modern contract and
// preserves unknown future result types as opaque observations. Callers decide
// whether they can continue; this package never submits inputResponses or
// retries.
func ParseMRTR(raw json.RawMessage) (MRTR, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return MRTR{}, fmt.Errorf("%w: result must be an object", ErrInvalidMRTR)
	}
	var resultType string
	if err := json.Unmarshal(fields["resultType"], &resultType); err != nil || resultType == "" {
		return MRTR{}, fmt.Errorf("%w: resultType is required", ErrInvalidMRTR)
	}
	parsed := MRTR{Type: ResultType(resultType), Raw: append(json.RawMessage(nil), raw...)}
	parsed.InputRequests = cloneRaw(fields["inputRequests"])
	parsed.RequestState = cloneRaw(fields["requestState"])
	parsed.InputResponses = cloneRaw(fields["inputResponses"])
	if parsed.Type == ResultInputRequired {
		// The server result accepts only the request map and opaque state. A
		// client response map belongs on the retry request and must not make
		// an otherwise incomplete server result look valid.
		valid := false
		if parsed.InputRequests != nil {
			if string(parsed.InputRequests) == "null" {
				return MRTR{}, fmt.Errorf("%w: inputRequests must be an object", ErrInvalidMRTR)
			}
			var requests map[string]json.RawMessage
			if err := json.Unmarshal(parsed.InputRequests, &requests); err != nil || requests == nil {
				return MRTR{}, fmt.Errorf("%w: inputRequests must be an object", ErrInvalidMRTR)
			}
			valid = true
			for key, request := range requests {
				if err := validateInputRequest(key, request); err != nil {
					return MRTR{}, fmt.Errorf("%w: invalid input request %q: %v", ErrInvalidMRTR, key, err)
				}
			}
		}
		if parsed.RequestState != nil && string(parsed.RequestState) != "null" {
			var state string
			if err := json.Unmarshal(parsed.RequestState, &state); err != nil {
				return MRTR{}, fmt.Errorf("%w: requestState must be a string", ErrInvalidMRTR)
			}
			valid = true
		}
		if !valid {
			return MRTR{}, fmt.Errorf("%w: input_required needs a valid inputRequests map or requestState string", ErrInvalidMRTR)
		}
	}
	return parsed, nil
}

// validateInputRequest checks the small discriminated union that can appear
// in a server InputRequiredResult. The request bodies remain raw and are never
// executed; this only prevents malformed or unrelated objects from being
// mistaken for an official MCP round trip.
func validateInputRequest(key string, raw json.RawMessage) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("input request key is empty")
	}
	var request struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(raw, &request); err != nil || request.Method == "" {
		return fmt.Errorf("input request must have method and params")
	}
	if request.Params == nil || string(request.Params) == "null" {
		if request.Method == "roots/list" {
			return nil
		}
		return fmt.Errorf("%s requires params", request.Method)
	}
	var params map[string]json.RawMessage
	if json.Unmarshal(request.Params, &params) != nil || params == nil {
		return fmt.Errorf("%s params must be an object", request.Method)
	}
	switch request.Method {
	case "sampling/createMessage":
		var messages []json.RawMessage
		if json.Unmarshal(params["messages"], &messages) != nil || messages == nil {
			return fmt.Errorf("sampling/createMessage requires messages")
		}
		var maxTokens json.Number
		if err := json.Unmarshal(params["maxTokens"], &maxTokens); err != nil || maxTokens == "" {
			return fmt.Errorf("sampling/createMessage requires maxTokens")
		}
	case "roots/list":
		// The params object only carries optional _meta.
	case "elicitation/create":
		var message string
		if json.Unmarshal(params["message"], &message) != nil {
			return fmt.Errorf("elicitation/create requires message")
		}
		var mode string
		if rawMode, present := params["mode"]; present && json.Unmarshal(rawMode, &mode) != nil {
			return fmt.Errorf("elicitation/create mode must be a string")
		}
		if mode != "" && mode != "form" && mode != "url" {
			return fmt.Errorf("elicitation/create mode must be form or url")
		}
		if mode == "url" {
			var url string
			if json.Unmarshal(params["url"], &url) != nil || url == "" {
				return fmt.Errorf("elicitation/create url mode requires url")
			}
		} else {
			var schema map[string]json.RawMessage
			if json.Unmarshal(params["requestedSchema"], &schema) != nil || schema == nil {
				return fmt.Errorf("elicitation/create form mode requires requestedSchema")
			}
			var typ string
			if json.Unmarshal(schema["type"], &typ) != nil || typ != "object" {
				return fmt.Errorf("elicitation/create requestedSchema must be an object schema")
			}
			var properties map[string]json.RawMessage
			if json.Unmarshal(schema["properties"], &properties) != nil || properties == nil {
				return fmt.Errorf("elicitation/create requestedSchema requires properties")
			}
		}
	default:
		return fmt.Errorf("unsupported input request method %q", request.Method)
	}
	return nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}
