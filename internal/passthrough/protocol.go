package passthrough

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Tutitoos/atenea/internal/mcpcompat"
)

// ProtocolMode is part of ATENEA's public orchestration contract.
type ProtocolMode string

type modernHeaderValuesKey struct{}

const (
	// ProtocolLegacy is part of ATENEA's public orchestration contract.
	ProtocolLegacy ProtocolMode = "legacy"
	// ProtocolAuto is part of ATENEA's public orchestration contract.
	ProtocolAuto ProtocolMode = "auto"
	// ProtocolModernPin is part of ATENEA's public orchestration contract.
	ProtocolModernPin ProtocolMode = "modern-pin"
)

func normalizedProtocolMode(mode ProtocolMode) ProtocolMode {
	if mode == "" {
		return ProtocolLegacy
	}
	if mode != ProtocolLegacy && mode != ProtocolAuto && mode != ProtocolModernPin {
		return ProtocolLegacy
	}
	return mode
}

func requestedEra(mode ProtocolMode) mcpcompat.Era {
	if normalizedProtocolMode(mode) == ProtocolAuto || normalizedProtocolMode(mode) == ProtocolModernPin {
		return mcpcompat.Modern
	}
	return mcpcompat.Legacy
}

var errProtocolFallback = errors.New("passthrough: protocol compatibility fallback")

// ErrInputRequired is part of ATENEA's public orchestration contract.
var ErrInputRequired = errors.New("passthrough: modern response requires input")

// InputRequiredError preserves the complete MRTR response. Its fields are
// provider data, never instructions for Atenea to execute or answer.
type InputRequiredError struct {
	Response mcpcompat.MRTR
}

func (e *InputRequiredError) Error() string { return ErrInputRequired.Error() }
func (e *InputRequiredError) Unwrap() error { return ErrInputRequired }

type protocolFallbackError struct{ message string }

func (e *protocolFallbackError) Error() string { return e.message }
func (e *protocolFallbackError) Unwrap() error { return errProtocolFallback }

func isProtocolFallback(err error) bool {
	var typed *protocolFallbackError
	return errors.As(err, &typed)
}

func modernParams(params any) (map[string]any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, fmt.Errorf("modern MCP params must be an object: %w", err)
		}
	}
	meta := map[string]any{}
	if existing, ok := out["_meta"].(map[string]any); ok {
		for key, value := range existing {
			meta[key] = value
		}
	}
	meta[mcpcompat.ProtocolVersionKey] = mcpcompat.Modern.String()
	meta[mcpcompat.ClientInfoKey] = map[string]any{"name": "atenea", "version": "1"}
	meta[mcpcompat.ClientCapabilitiesKey] = map[string]any{}
	out["_meta"] = meta
	return out, nil
}

func modernMessageName(params any) string {
	raw, err := json.Marshal(params)
	if err != nil {
		return ""
	}
	var fields struct {
		Name   string `json:"name"`
		URI    string `json:"uri"`
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal(raw, &fields) != nil {
		return ""
	}
	for _, value := range []string{fields.Name, fields.URI, fields.TaskID} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validateModernResult(raw json.RawMessage, list bool) error {
	result, err := mcpcompat.ParseMRTR(raw)
	if err != nil {
		return fmt.Errorf("modern MCP: %w", err)
	}
	if result.Type == mcpcompat.ResultInputRequired {
		return &InputRequiredError{Response: result}
	}
	if result.Type != mcpcompat.ResultComplete {
		return &mcpcompat.FutureResultError{Response: result}
	}
	if list {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return errors.New("modern MCP result is not an object")
		}
		var ttl int64
		if err := json.Unmarshal(envelope["ttlMs"], &ttl); err != nil || ttl < 0 {
			return errors.New("modern tools/list result has invalid ttlMs")
		}
		var scope string
		if err := json.Unmarshal(envelope["cacheScope"], &scope); err != nil || (scope != "public" && scope != "private") {
			return errors.New("modern tools/list result has invalid cacheScope")
		}
	}
	return nil
}

func modernCacheHint(raw json.RawMessage) (cacheHint, error) {
	result, err := mcpcompat.ParseMRTR(raw)
	if err != nil {
		return cacheHint{}, err
	}
	if result.Type != mcpcompat.ResultComplete {
		return cacheHint{}, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return cacheHint{}, err
	}
	var ttl int64
	if err := json.Unmarshal(envelope["ttlMs"], &ttl); err != nil || ttl < 0 || ttl > mcpcompat.MaxCacheTTLMS {
		return cacheHint{}, errors.New("modern tools/list result has invalid ttlMs")
	}
	var scope string
	if err := json.Unmarshal(envelope["cacheScope"], &scope); err != nil || (scope != "public" && scope != "private") {
		return cacheHint{}, errors.New("modern tools/list result has invalid cacheScope")
	}
	if ttl == 0 {
		return cacheHint{}, nil
	}
	return cacheHint{Cache: true, Duration: time.Duration(ttl) * time.Millisecond}, nil
}

// headerAnnotations validates the optional HTTP header extension while the
// catalog is being read. Invalid annotations remove one tool from the exposed
// catalog rather than making the whole backend unusable.
func headerAnnotations(schema json.RawMessage) (map[string]string, bool) {
	if len(bytes.TrimSpace(schema)) == 0 {
		// Older MCP servers may omit inputSchema entirely. An explicitly
		// supplied non-object schema is different: it is malformed and must
		// never be presented as a callable modern tool.
		return nil, true
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(schema, &root) != nil || root == nil {
		return nil, false
	}
	properties := map[string]json.RawMessage{}
	if raw := root["properties"]; raw != nil && json.Unmarshal(raw, &properties) != nil {
		return nil, false
	}
	result := map[string]string{}
	used := map[string]bool{}
	var visit func(map[string]json.RawMessage, []string) bool
	visit = func(properties map[string]json.RawMessage, path []string) bool {
		for name, raw := range properties {
			var property map[string]json.RawMessage
			if json.Unmarshal(raw, &property) != nil {
				return false
			}
			current := append(append([]string(nil), path...), name)
			if annotation, present := property["x-mcp-header"]; present {
				var header string
				if json.Unmarshal(annotation, &header) != nil || !validHeaderToken(header) || used[strings.ToLower(header)] {
					return false
				}
				var typ string
				_ = json.Unmarshal(property["type"], &typ)
				if typ != "string" && typ != "integer" && typ != "boolean" {
					return false
				}
				used[strings.ToLower(header)] = true
				result[strings.Join(current, ".")] = header
			}
			var nested map[string]json.RawMessage
			if rawNested := property["properties"]; rawNested != nil {
				if json.Unmarshal(rawNested, &nested) != nil || !visit(nested, current) {
					return false
				}
			}
		}
		return true
	}
	return result, visit(properties, nil)
}

func modernInputSchema(schema json.RawMessage) bool {
	var object map[string]json.RawMessage
	if len(bytes.TrimSpace(schema)) == 0 || json.Unmarshal(schema, &object) != nil || object == nil {
		return false
	}
	var typ string
	return json.Unmarshal(object["type"], &typ) == nil && typ == "object"
}

func validHeaderToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > 127 || (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", r) {
			return false
		}
	}
	return true
}

func headerValues(annotations map[string]string, arguments map[string]any) (map[string]string, error) {
	values := make(map[string]string)
	for path, header := range annotations {
		var current any = arguments
		found := true
		for _, name := range strings.Split(path, ".") {
			object, ok := current.(map[string]any)
			if !ok {
				found = false
				break
			}
			current, found = object[name]
			if !found {
				break
			}
		}
		if !found || current == nil {
			continue
		}
		switch value := current.(type) {
		case string:
			values[header] = encodeHeaderValue(value)
		case bool:
			values[header] = strconv.FormatBool(value)
		case float64:
			if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || math.Abs(value) > 9007199254740991 {
				return nil, fmt.Errorf("integer header parameter %q is outside the safe IEEE-754 range", path)
			}
			values[header] = strconv.FormatFloat(value, 'f', -1, 64)
		case json.Number:
			integer, err := strconv.ParseInt(string(value), 10, 64)
			if err != nil || math.Abs(float64(integer)) > 9007199254740991 {
				return nil, fmt.Errorf("integer header parameter %q is outside the safe IEEE-754 range", path)
			}
			values[header] = strconv.FormatInt(integer, 10)
		case int:
			if math.Abs(float64(value)) > 9007199254740991 {
				return nil, fmt.Errorf("integer header parameter %q is outside the safe IEEE-754 range", path)
			}
			values[header] = strconv.Itoa(value)
		case int8:
			values[header] = strconv.FormatInt(int64(value), 10)
		case int16:
			values[header] = strconv.FormatInt(int64(value), 10)
		case int32:
			values[header] = strconv.FormatInt(int64(value), 10)
		case int64:
			if math.Abs(float64(value)) > 9007199254740991 {
				return nil, fmt.Errorf("integer header parameter %q is outside the safe IEEE-754 range", path)
			}
			values[header] = strconv.FormatInt(value, 10)
		default:
			return nil, fmt.Errorf("header parameter %q is not primitive", path)
		}
	}
	return values, nil
}

func encodeHeaderValue(value string) string {
	return mcpcompat.EncodeSentinelValue(value)
}

func modernServerInfo(raw json.RawMessage) (name, version string) {
	var envelope struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", ""
	}
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if json.Unmarshal(envelope.Meta[mcpcompat.ServerInfoKey], &info) != nil {
		return "", ""
	}
	return strings.TrimSpace(info.Name), strings.TrimSpace(info.Version)
}

func isLegacyDiscovery(raw json.RawMessage) bool {
	var body struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	return json.Unmarshal(raw, &body) == nil && body.ProtocolVersion == mcpcompat.Legacy.String()
}
