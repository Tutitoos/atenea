package agentdevice

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
)

//go:embed testdata/*-0.20.10.json
var schemas embed.FS

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

var refPattern = regexp.MustCompile(`^@e[0-9]+$`)

// Validate applies only rules qualified against the observed release/schema.
// It never changes arguments or the upstream schema.
func Validate(version, tool string, schema json.RawMessage, args map[string]any) error {
	if tool != "wait" && tool != "click" {
		return nil
	}
	known, _ := schemas.ReadFile("testdata/" + tool + "-" + Version + ".json")
	if strings.TrimPrefix(version, "v") != Version || Fingerprint(schema) != Fingerprint(known) {
		return fmt.Errorf("agent-device compatibility unverified: version=%q schema=%s; expected %s. Run doctor before applying version-specific rules", version, Fingerprint(schema), Version)
	}
	invalid := func(reason string) error { return fmt.Errorf("%s. %s", reason, Help(tool)) }
	if err := validatePinnedSchema(schema, args); err != nil {
		return invalid(err.Error())
	}
	if tool == "wait" {
		condition, count := "", 0
		for _, key := range []string{"durationMs", "text", "ref", "selector", "stable"} {
			if _, exists := args[key]; exists {
				condition = key
				count++
			}
		}
		if count != 1 {
			return invalid("wait requires exactly one of durationMs, text, ref, selector, stable")
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
		if condition == "text" || condition == "ref" || condition == "selector" {
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
	} else {
		target, ok := args["target"].(map[string]any)
		if !ok {
			return invalid("click requires a discriminated target object")
		}
		switch target["kind"] {
		case "ref":
			ref, _ := target["ref"].(string)
			if !refPattern.MatchString(ref) {
				return invalid("target.ref must use @eN from the current session snapshot")
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
	return nil
}

// Help provides corrected examples without rewriting raw tool descriptions.
func Help(tool string) string {
	if tool == "click" {
		return `Example: {"session":"my-task","target":{"kind":"ref","ref":"@e12"}}. Use a fresh snapshot of that session; never retry an uncertain click automatically.`
	}
	return `Examples: {"session":"my-task","kind":"duration","durationMs":1000} or {"session":"my-task","kind":"stable","stable":true,"quietMs":500}. List sessions with atenea.command name=device.sessions. Keep session, cwd and device explicit; do not take another task's session.`
}
