package agentdevice

import (
	"encoding/json"
	"maps"
	"reflect"
	"testing"
)

func candidateSchema(t *testing.T, tool string) json.RawMessage {
	t.Helper()
	raw, err := schemas.ReadFile("testdata/" + tool + "-" + CandidateVersion + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestCandidateCaptureCoversTheWholeCatalogWithoutWideningIt(t *testing.T) {
	raw := candidateSchema(t, "catalog")
	var capture struct {
		ToolCount int      `json:"tool_count"`
		Added     []string `json:"added_tools"`
		Removed   []string `json:"removed_tools"`
		Tools     []struct {
			Name      string `json:"name"`
			SHA       string `json:"input_schema_sha256"`
			Supported bool   `json:"supported"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	if capture.ToolCount != 59 || len(capture.Tools) != 59 || len(capture.Removed) != 0 || !reflect.DeepEqual(capture.Added, []string{"action-button", "fold"}) {
		t.Fatal("incomplete catalog capture")
	}
	supported := 0
	seen := map[string]bool{}
	for _, tool := range capture.Tools {
		if seen[tool.Name] {
			t.Fatalf("duplicate %s", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Supported != CandidateAllows(tool.Name) {
			t.Fatalf("catalog budget mismatch for %s", tool.Name)
		}
		if tool.Supported {
			supported++
			if Fingerprint(candidateSchema(t, tool.Name)) != tool.SHA {
				t.Fatalf("capture fingerprint changed for %s", tool.Name)
			}
		}
	}
	if supported != 22 {
		t.Fatalf("qualified tools=%d", supported)
	}
	for _, tool := range Tools() {
		if !seen[tool] {
			t.Fatalf("baseline tool %s omitted", tool)
		}
	}
	for _, tool := range []string{"action-button", "fold", "batch", "replay", "test", "settings", "shutdown"} {
		if CandidateAllows(tool) {
			t.Fatalf("unreviewed tool %s admitted", tool)
		}
		if err := Validate(CandidateVersion, tool, json.RawMessage(`{}`), map[string]any{"cwd": "/fixture"}); err == nil {
			t.Fatalf("unreviewed %s allowed before dispatch", tool)
		}
	}
}

func TestCandidatePreservesLocalContextButSendsOnlyUpstreamArguments(t *testing.T) {
	for _, tool := range []string{"open", "click", "fill"} {
		args := map[string]any{"session": "flow", "cwd": "/fixture"}
		if tool == "open" {
			args["serial"] = "fixture-device"
			args["waitMs"] = float64(100)
		} else {
			args["target"] = map[string]any{"kind": "ref", "ref": "@e1~s2"}
		}
		if tool == "fill" {
			args["text"] = ""
		}
		schema := candidateSchema(t, tool)
		if err := Validate(CandidateVersion, tool, schema, args); err != nil {
			t.Fatal(err)
		}
		advertised, err := AdvertisedSchema(CandidateVersion, tool, schema)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateSchema(advertised, args, "arguments"); err != nil {
			t.Fatal(err)
		}
		wire := WireArguments(CandidateVersion, args)
		if _, sent := wire["cwd"]; sent || args["cwd"] != "/fixture" {
			t.Fatal("wire context leak or local mutation")
		}
		if err := validatePinnedSchema(schema, wire); err != nil {
			t.Fatalf("invalid wire args: %v", err)
		}
		for _, field := range []string{"stateDir", "daemonBaseUrl", "daemonAuthToken", "iosXctestEnvDir"} {
			bad := maps.Clone(args)
			bad[field] = "operator-override"
			if err := Validate(CandidateVersion, tool, schema, bad); err == nil {
				t.Fatalf("%s accepted runtime %s", tool, field)
			}
			if _, hidden := WireArguments(CandidateVersion, bad)[field]; !hidden {
				t.Fatal("unsupported field silently removed")
			}
		}
		if err := Validate("0.21.24", tool, schema, args); err == nil {
			t.Fatal("unknown release accepted")
		}
		if err := Validate(CandidateVersion, tool, json.RawMessage(`{"type":"object"}`), args); err == nil {
			t.Fatal("schema drift accepted")
		}
		if tool == "open" {
			properties := advertised["properties"].(map[string]any)
			for _, row := range advertised["anyOf"].([]any) {
				branch := row.(map[string]any)
				if len(branch["properties"].(map[string]any)) != len(properties) {
					t.Fatal("selector branch loses fields")
				}
			}
		}
	}
}

func TestCandidateWaitAbsentIsVersionScopedAndExclusive(t *testing.T) {
	args := map[string]any{"cwd": "/fixture", "session": "flow", "kind": "absent", "absent": "role=button"}
	if err := Validate(CandidateVersion, "wait", candidateSchema(t, "wait"), args); err != nil {
		t.Fatal(err)
	}
	old, _ := schemas.ReadFile("testdata/wait-" + Version + ".json")
	if err := Validate(Version, "wait", old, args); err == nil {
		t.Fatal("candidate wait leaked to baseline")
	}
	for _, bad := range []map[string]any{
		{"cwd": "/fixture", "absent": ""},
		{"cwd": "/fixture", "absent": "role=button", "stable": true},
		{"cwd": "/fixture", "kind": "selector", "absent": "role=button"},
	} {
		if err := Validate(CandidateVersion, "wait", candidateSchema(t, "wait"), bad); err == nil {
			t.Fatalf("invalid wait accepted: %v", bad)
		}
	}
}
