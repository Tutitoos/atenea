package agentdevice

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"strings"
)

//go:embed testdata/*-0.20.10.json testdata/*-0.21.23.json
var schemas embed.FS

// IsCandidate reports whether the observed release has the bounded contract.
func IsCandidate(version string) bool { return strings.TrimPrefix(version, "v") == CandidateVersion }

// VerifySchema verifies exact releases and input schemas; it does not authorize
// a tool or claim device acceptance. The old release keeps its existing scope.
func VerifySchema(version, tool string, upstream json.RawMessage) error {
	version = strings.TrimPrefix(version, "v")
	if version != Version && version != CandidateVersion {
		return fmt.Errorf("agent-device compatibility unverified: version=%q; supported %s, %s", version, Version, CandidateVersion)
	}
	if version == CandidateVersion && !CandidateAllows(tool) {
		return fmt.Errorf("agent-device compatibility unverified: %s %s is outside the qualified core catalog", version, tool)
	}
	known, err := schemas.ReadFile("testdata/" + tool + "-" + version + ".json")
	if err != nil || Fingerprint(upstream) == "" || Fingerprint(upstream) != Fingerprint(known) {
		return fmt.Errorf("agent-device compatibility unverified: version=%q tool=%s schema=%s", version, tool, Fingerprint(upstream))
	}
	return nil
}

// WireArguments removes only Atenea's locally checked cwd for 0.21.23. All
// operator-only realm/runner fields remain invalid; they are never stripped.
// Callers must first verify the schema and the static workspace binding.
func WireArguments(version string, args map[string]any) map[string]any {
	if !IsCandidate(version) {
		return args
	}
	out := maps.Clone(args)
	delete(out, "cwd")
	return out
}

// Fingerprint ignores JSON object ordering while preserving the schema itself.
func Fingerprint(raw json.RawMessage) string {
	var decoded any
	if json.Unmarshal(raw, &decoded) != nil {
		return ""
	}
	canonical, _ := json.Marshal(decoded)
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// The optional generation is emitted by agent-device 0.20.10 for mutation refs.
// Sixteen digits cover JavaScript's safe integer range without accepting an
// unbounded suffix; the device still decides whether the generation is fresh.
const refPatternSource = `^@e[0-9]+(?:~s[0-9]{1,16})?$`

var refPattern = regexp.MustCompile(refPatternSource)

// Validate applies only rules qualified against the observed release/schema.
// It never changes arguments or the upstream schema.
func Validate(version, tool string, schema json.RawMessage, args map[string]any) error {
	if strings.TrimPrefix(version, "v") == Version && tool != "wait" && tool != "click" && tool != "open" && tool != "fill" {
		return nil
	}
	if err := VerifySchema(version, tool, schema); err != nil {
		return err
	}
	invalid := func(reason string) error { return fmt.Errorf("%s. %s", reason, Help(tool)) }
	if IsCandidate(version) {
		if cwd, _ := args["cwd"].(string); !strings.HasPrefix(cwd, "/") {
			return invalid("cwd must be an explicit absolute path matching the configured working_directory")
		}
	}
	if err := validatePinnedSchema(schema, WireArguments(version, args)); err != nil {
		return invalid(err.Error())
	}
	switch tool {
	case "wait":
		condition, count := "", 0
		conditions := []string{"durationMs", "text", "ref", "selector", "stable"}
		if IsCandidate(version) {
			conditions = append(conditions, "absent")
		}
		for _, key := range conditions {
			if _, exists := args[key]; exists {
				condition = key
				count++
			}
		}
		if count != 1 {
			return invalid("wait requires exactly one supported wait condition")
		}
		kind := condition
		if condition == "durationMs" {
			kind = "duration"
		}
		if k, exists := args["kind"]; exists && k != kind {
			return invalid("kind must match the supplied wait condition")
		}
		if condition == "stable" && args[condition] != true {
			return invalid("stable must be true")
		}
		if condition == "text" || condition == "ref" || condition == "selector" || condition == "absent" {
			v, _ := args[condition].(string)
			if strings.TrimSpace(v) == "" {
				return invalid(condition + " must be nonempty")
			}
			if condition == "ref" && !refPattern.MatchString(v) {
				return invalid("ref must be a snapshot reference such as @e12")
			}
		}
		for _, key := range []string{"durationMs", "quietMs", "timeoutMs", "depth"} {
			if raw, exists := args[key]; exists {
				n, ok := raw.(float64)
				if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || math.Trunc(n) != n || (key == "timeoutMs" && n == 0) {
					return invalid(key + " must be a nonnegative integer (timeoutMs must be positive)")
				}
			}
		}
	case "click", "fill":
		target, ok := args["target"].(map[string]any)
		if !ok {
			return invalid(tool + " requires a discriminated target object")
		}
		switch target["kind"] {
		case "ref":
			ref, _ := target["ref"].(string)
			if !refPattern.MatchString(ref) {
				return invalid("target.ref must use @eN or @eN~sN from the current session snapshot")
			}
		case "selector":
			selector, _ := target["selector"].(string)
			if !strings.Contains(selector, "=") {
				return invalid("target.selector expects key=value; use kind=ref for @eN")
			}
		case "point":
			for _, key := range []string{"x", "y"} {
				n, ok := target[key].(float64)
				if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
					return invalid("point requires finite x and y coordinates")
				}
			}
		default:
			return invalid("target.kind must be ref, selector or point")
		}
	}
	if tool == "open" || tool == "click" || tool == "fill" {
		if session, _ := args["session"].(string); session == "" {
			return invalid("session must be explicit and nonempty")
		}
		if cwd, _ := args["cwd"].(string); !strings.HasPrefix(cwd, "/") {
			return invalid("cwd must be an explicit absolute path")
		}
		if tool == "open" {
			selected := false
			for _, key := range []string{"udid", "serial", "device"} {
				if s, _ := args[key].(string); s != "" {
					selected = true
				}
			}
			if !selected {
				return invalid("open requires an explicit udid, serial or device")
			}
		}
	}
	return nil
}

// Help provides corrected examples without rewriting raw tool descriptions.
func Help(tool string) string {
	if tool == "click" {
		return `Example: {"session":"my-task","cwd":"/absolute/project","target":{"kind":"ref","ref":"@e12"}}. Use a fresh snapshot of that session; never retry an uncertain click automatically.`
	}
	if tool == "open" {
		return `Example: {"session":"my-task","cwd":"/absolute/project","udid":"explicit-device-id","app":"example.app"}. Choose a free device and a dedicated session.`
	}
	if tool == "fill" {
		return `Example: {"session":"my-task","cwd":"/absolute/project","target":{"kind":"ref","ref":"@e12"},"text":"value"}. Use a fresh snapshot; do not include private text in diagnostics.`
	}
	return `Examples: {"session":"my-task","kind":"duration","durationMs":1000} or {"session":"my-task","kind":"stable","stable":true,"quietMs":500}. List sessions with atenea.command name=device.sessions. Keep session, cwd and device explicit; do not take another task's session.`
}
