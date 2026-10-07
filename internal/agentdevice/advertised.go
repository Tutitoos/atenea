package agentdevice

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AdvertisedSchema decorates only the verified upstream release. The returned
// map is detached from the upstream bytes; callers must not mutate the latter.
// A changed upstream schema is withheld until its contract is reviewed.
func AdvertisedSchema(version, tool string, upstream json.RawMessage) (map[string]any, error) {
	if tool != "open" && tool != "click" && tool != "fill" {
		return nil, fmt.Errorf("agent-device %s has no advertised adaptation", tool)
	}
	known, err := schemas.ReadFile("testdata/" + tool + "-" + Version + ".json")
	if err != nil || strings.TrimPrefix(version, "v") != Version || Fingerprint(upstream) != Fingerprint(known) {
		return nil, fmt.Errorf("agent-device %s schema/version is unverified", tool)
	}
	var out map[string]any
	if err := json.Unmarshal(upstream, &out); err != nil {
		return nil, err
	}
	required, _ := out["required"].([]any)
	out["required"] = append(required, "session", "cwd")
	properties := out["properties"].(map[string]any)
	properties["session"].(map[string]any)["minLength"] = float64(1)
	properties["cwd"].(map[string]any)["pattern"] = `^/`
	properties["cwd"].(map[string]any)["description"] = "Explicit absolute working directory for this flow."
	if tool == "open" {
		selectors := []any{}
		for _, key := range []string{"udid", "serial", "device"} {
			selectors = append(selectors, map[string]any{
				"type": "object", "required": []string{key},
				"properties": map[string]any{key: map[string]any{"type": "string", "minLength": float64(1)}},
			})
		}
		out["anyOf"] = selectors
	}
	if tool == "click" || tool == "fill" {
		target := properties["target"].(map[string]any)
		for _, item := range target["oneOf"].([]any) {
			variant := item.(map[string]any)
			fields := variant["properties"].(map[string]any)
			switch fields["kind"].(map[string]any)["const"] {
			case "ref":
				fields["ref"].(map[string]any)["pattern"] = refPatternSource
				fields["ref"].(map[string]any)["description"] = "Snapshot reference such as @e12 or @e12~s4 (pinned to refsGeneration). Use a fresh snapshot of this session; the pattern alone cannot establish freshness."
			case "selector":
				fields["selector"].(map[string]any)["pattern"] = `=`
			}
		}
	}
	return out, nil
}

// AdvertisedDescription adds the live checks that JSON Schema cannot express.
func AdvertisedDescription(tool, upstream string) string {
	return upstream + " ATENEA requires an explicit dedicated session and absolute cwd. Session ownership and live device state are checked before dispatch; JSON Schema cannot establish them. " +
		map[string]string{
			"open":  "Choose an explicit free udid, serial or device. An uncertain open must be observed before retrying.",
			"click": "Use a fresh snapshot for a ref. An uncertain click must be observed before retrying.",
			"fill":  "Use a fresh snapshot for a ref. Do not log private text or retry an uncertain fill automatically.",
		}[tool]
}
