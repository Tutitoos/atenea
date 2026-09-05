package agentdevice

import (
	"encoding/json"
	"testing"
)

func TestPinnedValidationRejectsMalformedCalls(t *testing.T) {
	for _, tc := range []struct{ tool, raw string }{
		{"wait", `{}`}, {"wait", `{"durationMs":1,"stable":true}`},
		{"wait", `{"kind":"text","durationMs":1}`}, {"wait", `{"durationMs":-1}`},
		{"wait", `{"stable":false}`}, {"wait", `{"text":""}`}, {"wait", `{"ref":"e12"}`},
		{"click", `{"target":"e12"}`}, {"click", `{"target":{"kind":"ref","ref":"e12"}}`},
		{"click", `{"target":{"kind":"selector","selector":"e12"}}`},
		{"click", `{"target":{"kind":"point","x":1}}`},
	} {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.raw), &args)
		schema, _ := schemas.ReadFile("testdata/" + tc.tool + "-" + Version + ".json")
		if err := Validate(Version, tc.tool, schema, args); err == nil {
			t.Fatalf("accepted %s %s", tc.tool, tc.raw)
		}
	}
}

func TestPinnedValidationPreservesValidVariants(t *testing.T) {
	for _, tc := range []struct{ tool, raw string }{
		{"wait", `{"durationMs":0}`}, {"wait", `{"kind":"stable","stable":true,"quietMs":500}`},
		{"wait", `{"text":"Ready"}`}, {"wait", `{"ref":"@e12"}`}, {"wait", `{"selector":"role=button"}`},
		{"click", `{"target":{"kind":"ref","ref":"@e12"}}`},
		{"click", `{"target":{"kind":"selector","selector":"role=button"}}`},
		{"click", `{"target":{"kind":"point","x":1,"y":2}}`},
	} {
		var args map[string]any
		_ = json.Unmarshal([]byte(tc.raw), &args)
		schema, _ := schemas.ReadFile("testdata/" + tc.tool + "-" + Version + ".json")
		before, _ := json.Marshal(args)
		if err := Validate(Version, tc.tool, schema, args); err != nil {
			t.Fatal(err)
		}
		after, _ := json.Marshal(args)
		if string(before) != string(after) {
			t.Fatal("arguments changed")
		}
		if err := Validate("99.0", tc.tool, schema, args); err == nil {
			t.Fatal("unqualified version accepted")
		}
		if err := Validate(Version, tc.tool, json.RawMessage(`{}`), args); err == nil {
			t.Fatal("schema drift accepted")
		}
	}
}

func TestPinnedSchemaRejectsMixedClickAndWrongScalarTypes(t *testing.T) {
	schema, _ := schemas.ReadFile("testdata/click-" + Version + ".json")
	for _, args := range []map[string]any{
		{"target": map[string]any{"kind": "ref", "ref": "@e1", "x": float64(5)}},
		{"target": map[string]any{"kind": "point", "x": float64(1), "y": float64(2)}, "count": float64(0)},
		{"target": map[string]any{"kind": "ref", "ref": "@e1"}, "verify": "true"},
		{"target": map[string]any{"kind": "ref", "ref": "@e1"}, "platform": "unknown"},
		{"target": map[string]any{"kind": "ref", "ref": "@e1"}, "invented": true},
	} {
		if err := Validate(Version, "click", schema, args); err == nil {
			t.Fatalf("invalid schema accepted: %#v", args)
		}
	}
}
