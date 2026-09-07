// Package mcpcompat defines the MCP protocol eras Atenea recognizes.
// Transport sessions and application sessions remain outside this package.
package mcpcompat

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	// Legacy is part of ATENEA's public orchestration contract.
	Legacy Era = "2025-06-18"
	// Modern is part of ATENEA's public orchestration contract.
	Modern Era = "2026-07-28"
	// Unknown is part of ATENEA's public orchestration contract.
	Unknown Era = "unknown"
	// ProtocolVersionKey is part of ATENEA's public orchestration contract.
	ProtocolVersionKey = "io.modelcontextprotocol/protocolVersion"
	// ClientInfoKey is part of ATENEA's public orchestration contract.
	ClientInfoKey = "io.modelcontextprotocol/clientInfo"
	// ClientCapabilitiesKey is part of ATENEA's public orchestration contract.
	ClientCapabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
	// ServerInfoKey is part of ATENEA's public orchestration contract.
	ServerInfoKey = "io.modelcontextprotocol/serverInfo"
	// MaxCacheTTLMS is part of ATENEA's public orchestration contract.
	MaxCacheTTLMS int64 = (1<<63 - 1) / int64(time.Millisecond)
)

// ErrUnsupportedVersion is part of ATENEA's public orchestration contract.
var ErrUnsupportedVersion = errors.New("unsupported MCP protocol version")

// ErrMissingModernMetadata is part of ATENEA's public orchestration contract.
var ErrMissingModernMetadata = errors.New("modern MCP request requires protocolVersion and clientCapabilities in params._meta")

// Era is an exact MCP revision. Unknown is used for an empty or unverified
// observation and is never accepted by Parse.
type Era string

func (e Era) String() string { return string(e) }

// Supported returns the exact revisions implemented by this build, in
// preference order. A fresh slice is returned so callers cannot mutate the
// policy globally.
func Supported() []Era { return slices.Clone([]Era{Modern, Legacy}) }

// Parse is part of ATENEA's public orchestration contract.
func Parse(value string) (Era, error) {
	switch value {
	case string(Legacy):
		return Legacy, nil
	case string(Modern):
		return Modern, nil
	default:
		return Unknown, fmt.Errorf("%w: %q", ErrUnsupportedVersion, value)
	}
}

// Validate is part of ATENEA's public orchestration contract.
func Validate(value string) error {
	_, err := Parse(value)
	return err
}

// RequiresInitialize is part of ATENEA's public orchestration contract.
func RequiresInitialize(era Era) bool { return era == Legacy }

// IsStateless is part of ATENEA's public orchestration contract.
func IsStateless(era Era) bool { return era == Modern }

// ModernMetadata is request context. Capabilities are retained as raw data so
// the server can observe them without interpreting them as permission grants.
type ModernMetadata struct {
	Version       Era
	ClientName    string
	ClientVersion string
	Capabilities  json.RawMessage
}

// HasProtocolMetadata reports whether params explicitly attempts protocol
// negotiation. Only the reserved key is recognized; flat lookalikes remain
// ordinary application data and never negotiate a protocol.
func HasProtocolMetadata(params json.RawMessage) bool {
	var envelope struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &envelope) != nil {
		return false
	}
	_, ok := envelope.Meta[ProtocolVersionKey]
	return ok
}

// ParseModernMetadata validates the complete modern per-request context.
// Unknown, missing, and malformed values are errors; no version is inferred.
func ParseModernMetadata(params json.RawMessage) (ModernMetadata, error) {
	var envelope struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if json.Unmarshal(params, &envelope) != nil || envelope.Meta == nil {
		return ModernMetadata{}, ErrMissingModernMetadata
	}
	var version string
	if err := json.Unmarshal(envelope.Meta[ProtocolVersionKey], &version); err != nil {
		return ModernMetadata{}, ErrMissingModernMetadata
	}
	era, err := Parse(version)
	if err != nil {
		return ModernMetadata{}, err
	}
	if era != Modern {
		return ModernMetadata{}, fmt.Errorf("modern request requires %s, got %s", Modern, era)
	}
	var identity struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if raw, present := envelope.Meta[ClientInfoKey]; present {
		if err := json.Unmarshal(raw, &identity); err != nil || strings.TrimSpace(identity.Name) == "" || strings.TrimSpace(identity.Version) == "" {
			return ModernMetadata{}, ErrMissingModernMetadata
		}
	}
	capabilities, ok := envelope.Meta[ClientCapabilitiesKey]
	if !ok || len(capabilities) == 0 || string(capabilities) == "null" || string(capabilities) == "false" {
		return ModernMetadata{}, ErrMissingModernMetadata
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(capabilities, &object) != nil {
		return ModernMetadata{}, ErrMissingModernMetadata
	}
	return ModernMetadata{Version: era, ClientName: strings.TrimSpace(identity.Name), ClientVersion: strings.TrimSpace(identity.Version), Capabilities: append(json.RawMessage(nil), capabilities...)}, nil
}

// RequestedObserved keeps what the caller selected separate from what the
// peer actually demonstrated. Zero values are exposed as Unknown rather than
// being mistaken for a successful negotiation.
type RequestedObserved struct {
	Requested Era `json:"requested"`
	Observed  Era `json:"observed"`
}

// NewRequestedObserved is part of ATENEA's public orchestration contract.
func NewRequestedObserved() RequestedObserved {
	return RequestedObserved{Requested: Unknown, Observed: Unknown}
}

// RequestedOrUnknown is part of ATENEA's public orchestration contract.
func (r RequestedObserved) RequestedOrUnknown() Era {
	if r.Requested == "" {
		return Unknown
	}
	return r.Requested
}

// ObservedOrUnknown is part of ATENEA's public orchestration contract.
func (r RequestedObserved) ObservedOrUnknown() Era {
	if r.Observed == "" {
		return Unknown
	}
	return r.Observed
}

// SetRequested is part of ATENEA's public orchestration contract.
func (r *RequestedObserved) SetRequested(value string) error {
	era, err := Parse(value)
	if err != nil {
		r.Requested = Unknown
		return err
	}
	r.Requested = era
	return nil
}

// SetObserved records only a verifiable supported peer version. Empty or
// unknown values become Unknown instead of being silently copied from the
// requested version.
func (r *RequestedObserved) SetObserved(value string) error {
	if strings.TrimSpace(value) == "" {
		r.Observed = Unknown
		return nil
	}
	era, err := Parse(value)
	if err != nil {
		r.Observed = Unknown
		return err
	}
	r.Observed = era
	return nil
}
