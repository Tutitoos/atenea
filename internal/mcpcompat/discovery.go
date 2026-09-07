package mcpcompat

import (
	"encoding/json"
	"fmt"
)

// Discovery is the validated modern server/discover envelope. A peer is not
// modern merely because it returned resultType=complete: it must advertise
// supported versions and the cacheable result fields required by the schema.
type Discovery struct {
	MRTR              MRTR
	SupportedVersions []Era
	Capabilities      Capabilities
	CapabilitiesRaw   json.RawMessage
	TTLMS             int64
	CacheScope        string
}

// Capability is the typed shape shared by MCP capability declarations. The
// pointer on Capabilities distinguishes an announced capability from one that
// is absent; an empty object is still an announcement.
type Capability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// Capabilities is part of ATENEA's public orchestration contract.
type Capabilities struct {
	Tools       *Capability `json:"tools"`
	Prompts     *Capability `json:"prompts"`
	Resources   *Capability `json:"resources"`
	Logging     *Capability `json:"logging"`
	Completions *Capability `json:"completions"`
	Tasks       *Capability `json:"tasks"`
}

// SupportsTools is part of ATENEA's public orchestration contract.
func (d Discovery) SupportsTools() bool { return d.Capabilities.Tools != nil }

// ParseDiscovery is part of ATENEA's public orchestration contract.
func ParseDiscovery(raw json.RawMessage) (Discovery, error) {
	mrtr, err := ParseMRTR(raw)
	if err != nil {
		return Discovery{}, err
	}
	if mrtr.Type != ResultComplete {
		return Discovery{}, fmt.Errorf("modern discovery must be complete")
	}
	var envelope struct {
		SupportedVersions []string        `json:"supportedVersions"`
		Capabilities      json.RawMessage `json:"capabilities"`
		TTLMS             *int64          `json:"ttlMs"`
		CacheScope        string          `json:"cacheScope"`
		Preferred         json.RawMessage `json:"preferred"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.SupportedVersions) == 0 || envelope.TTLMS == nil || *envelope.TTLMS < 0 || *envelope.TTLMS > MaxCacheTTLMS || envelope.CacheScope == "" || len(envelope.Capabilities) == 0 || envelope.Preferred != nil {
		return Discovery{}, fmt.Errorf("modern discovery requires supportedVersions, capabilities, ttlMs and cacheScope")
	}
	if envelope.CacheScope != "public" && envelope.CacheScope != "private" {
		return Discovery{}, fmt.Errorf("modern discovery has unsupported cacheScope %q", envelope.CacheScope)
	}
	var rawCapabilities map[string]json.RawMessage
	if json.Unmarshal(envelope.Capabilities, &rawCapabilities) != nil || rawCapabilities == nil {
		return Discovery{}, fmt.Errorf("modern discovery capabilities must be an object")
	}
	for _, name := range []string{"tools", "prompts", "resources", "logging", "completions", "tasks"} {
		raw, present := rawCapabilities[name]
		if !present {
			continue
		}
		if string(raw) == "null" {
			return Discovery{}, fmt.Errorf("modern discovery capability %s must be an object", name)
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(raw, &object) != nil || object == nil {
			return Discovery{}, fmt.Errorf("modern discovery capability %s must be an object", name)
		}
		if listChanged, present := object["listChanged"]; present {
			if string(listChanged) == "null" {
				return Discovery{}, fmt.Errorf("modern discovery capability %s.listChanged must be boolean", name)
			}
			var value bool
			if json.Unmarshal(listChanged, &value) != nil {
				return Discovery{}, fmt.Errorf("modern discovery capability %s.listChanged must be boolean", name)
			}
		}
	}
	var capabilities Capabilities
	if string(envelope.Capabilities) == "null" || json.Unmarshal(envelope.Capabilities, &capabilities) != nil {
		return Discovery{}, fmt.Errorf("modern discovery capabilities must be an object")
	}
	versions := make([]Era, 0, len(envelope.SupportedVersions))
	for _, value := range envelope.SupportedVersions {
		era, err := Parse(value)
		if err != nil {
			// A newer peer may advertise revisions this build does not know.
			// Negotiation is by intersection, so preserve known eras and keep
			// going as long as this build can select Modern.
			continue
		}
		versions = append(versions, era)
	}
	foundModern := false
	for _, era := range versions {
		foundModern = foundModern || era == Modern
	}
	if !foundModern {
		return Discovery{}, fmt.Errorf("modern discovery does not advertise %s", Modern)
	}
	return Discovery{MRTR: mrtr, SupportedVersions: versions, Capabilities: capabilities, CapabilitiesRaw: cloneRaw(envelope.Capabilities), TTLMS: *envelope.TTLMS, CacheScope: envelope.CacheScope}, nil
}
