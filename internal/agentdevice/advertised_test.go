package agentdevice

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestAdvertisedContractAgreesWithPinnedRuntime(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		valid   []string
		invalid []string
	}{
		{"open", []string{
			`{"session":"flow","cwd":"/project","udid":"ios-device"}`,
			`{"session":"flow","cwd":"/project","serial":"android-device","app":"example.app"}`,
			`{"session":"flow","cwd":"/project","device":"named-device"}`,
			`{"session":"flow","cwd":"/project","serial":"android-device","udid":"","saveScript":true}`,
			`{"session":"flow","cwd":"/project","serial":"android-device","saveScript":"recording.ad"}`,
		}, []string{
			`{"cwd":"/project","udid":"device"}`,
			`{"session":"flow","cwd":"relative","udid":"device"}`,
			`{"session":"flow","cwd":"/project"}`,
			`{"session":"flow","cwd":"/project","device":""}`,
			`{"session":"flow","cwd":"/project","device":"","serial":"","udid":""}`,
			`{"session":"flow","cwd":"/project","udid":7}`,
		}},
		{"click", []string{
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"selector","selector":"role=button"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"point","x":1,"y":2}}`,
		}, []string{
			`{"cwd":"/project","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"relative","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"selector","selector":"button"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"point","x":1}}`,
		}},
		{"fill", []string{
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12"},"text":"redacted"}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"point","x":1,"y":2},"text":"redacted"}`,
		}, []string{
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"e12"},"text":"redacted"}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"selector","selector":"button"},"text":"redacted"}`,
			`{"session":"flow","cwd":"relative","target":{"kind":"ref","ref":"@e12"},"text":"redacted"}`,
		}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			upstream, err := schemas.ReadFile("testdata/" + tc.tool + "-" + Version + ".json")
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(upstream)
			advertised, err := AdvertisedSchema(Version, tc.tool, upstream)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(upstream, original) {
				t.Fatal("upstream schema mutated")
			}
			for _, raw := range tc.valid {
				var args map[string]any
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					t.Fatal(err)
				}
				if err := validateSchema(advertised, args, "arguments"); err != nil {
					t.Fatalf("advertised rejected valid %s: %v", raw, err)
				}
				if err := Validate(Version, tc.tool, upstream, args); err != nil {
					t.Fatalf("runtime rejected valid %s: %v", raw, err)
				}
			}
			for _, raw := range tc.invalid {
				var args map[string]any
				if err := json.Unmarshal([]byte(raw), &args); err != nil {
					t.Fatal(err)
				}
				if err := validateSchema(advertised, args, "arguments"); err == nil {
					t.Fatalf("advertised accepted invalid %s", raw)
				}
				if err := Validate(Version, tc.tool, upstream, args); err == nil {
					t.Fatalf("runtime accepted invalid %s", raw)
				}
			}
			if _, err := AdvertisedSchema("0.20.11", tc.tool, upstream); err == nil {
				t.Fatal("version drift advertised")
			}
			if _, err := AdvertisedSchema(Version, tc.tool, json.RawMessage(`{"type":"object"}`)); err == nil {
				t.Fatal("schema drift advertised")
			}
			if !strings.Contains(AdvertisedDescription(tc.tool, "upstream"), "JSON Schema cannot establish") {
				t.Fatal("ownership limitation hidden")
			}
		})
	}
}

func TestFillFailureDoesNotEchoPrivateText(t *testing.T) {
	upstream, err := schemas.ReadFile("testdata/fill-" + Version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	const privateText = "fixture-private-value"
	args := map[string]any{
		"session": "flow", "cwd": "/project",
		"target": map[string]any{"kind": "ref", "ref": "stale"},
		"text":   privateText,
	}
	err = Validate(Version, "fill", upstream, args)
	if err == nil || strings.Contains(err.Error(), privateText) {
		t.Fatalf("fill failure missing or leaked text: %v", err)
	}
}

func TestUnsupportedFieldNameIsNotEchoed(t *testing.T) {
	upstream, err := schemas.ReadFile("testdata/fill-" + Version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	const privateKey = "private-token-fixture"
	args := map[string]any{
		"session": "flow", "cwd": "/project", "text": "value",
		"target":   map[string]any{"kind": "ref", "ref": "@e1"},
		privateKey: true,
	}
	err = Validate(Version, "fill", upstream, args)
	if err == nil || strings.Contains(err.Error(), privateKey) {
		t.Fatalf("unsupported field name leaked: %v", err)
	}
}

func TestUnrelatedToolHasNoAdvertisedAdaptation(t *testing.T) {
	wait, err := schemas.ReadFile("testdata/wait-" + Version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AdvertisedSchema(Version, "wait", wait); err == nil {
		t.Fatal("unrelated tool received the open/click/fill adaptation")
	}
}
