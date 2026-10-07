package agentdevice

import (
	"bytes"
	"encoding/json"
	"reflect"
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
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s4"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s9007199254740991"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"selector","selector":"role=button"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"point","x":1,"y":2}}`,
		}, []string{
			`{"cwd":"/project","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"relative","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s12345678901234567"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"selector","selector":"button"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"point","x":1}}`,
		}},
		{"fill", []string{
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12"},"text":"redacted"}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s4"},"text":"redacted"}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"point","x":1,"y":2},"text":"redacted"}`,
		}, []string{
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12"}}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"e12"},"text":"redacted"}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s-4"},"text":"redacted"}`,
			`{"session":"flow","cwd":"/project","target":{"kind":"ref","ref":"@e12~s4junk"},"text":"redacted"}`,
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

func TestOpenAnyOfBranchesProjectCompleteContract(t *testing.T) {
	upstream, err := schemas.ReadFile("testdata/open-" + Version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	advertised, err := AdvertisedSchema(Version, "open", upstream)
	if err != nil {
		t.Fatal(err)
	}
	rootProperties := advertised["properties"].(map[string]any)
	branches := advertised["anyOf"].([]any)
	for i, selector := range []string{"udid", "serial", "device"} {
		branch := branches[i].(map[string]any)
		properties := branch["properties"].(map[string]any)
		if len(properties) != len(rootProperties) || branch["additionalProperties"] != false {
			t.Fatalf("%s branch loses root fields or permits unknown fields", selector)
		}
		for name, rootProperty := range rootProperties {
			if _, ok := properties[name]; !ok {
				t.Fatalf("%s branch omits %s", selector, name)
			}
			if name != selector && !reflect.DeepEqual(properties[name], rootProperty) {
				t.Fatalf("%s branch changes %s", selector, name)
			}
		}
		required := branch["required"].([]string)
		if !reflect.DeepEqual(required, []string{"session", "cwd", selector}) {
			t.Fatalf("%s branch required fields = %v", selector, required)
		}
		args := map[string]any{"session": "flow", "cwd": "/project", selector: "fixture-device", "app": "example.app", "url": "https://example.test"}
		if selector == "serial" {
			args["udid"] = "" // A different, unused selector may be empty.
		}
		if err := validateSchema(branch, args, "arguments"); err != nil {
			t.Fatalf("%s projected branch rejected valid upstream options: %v", selector, err)
		}
		for _, missing := range []string{"session", "cwd", selector} {
			value := args[missing]
			delete(args, missing)
			if err := validateSchema(branch, args, "arguments"); err == nil {
				t.Fatalf("%s projected branch accepted missing %s", selector, missing)
			}
			args[missing] = value
		}
		args[selector] = ""
		if err := validateSchema(branch, args, "arguments"); err == nil {
			t.Fatalf("%s projected branch accepted empty selector", selector)
		}
		args[selector] = "fixture-device"
		args["invented"] = true
		if err := validateSchema(branch, args, "arguments"); err == nil {
			t.Fatalf("%s projected branch accepted unknown field", selector)
		}
	}
	first := branches[0].(map[string]any)["properties"].(map[string]any)
	second := branches[1].(map[string]any)["properties"].(map[string]any)
	first["app"].(map[string]any)["description"] = "changed only in first branch"
	if reflect.DeepEqual(first["app"], second["app"]) || reflect.DeepEqual(first["app"], rootProperties["app"]) {
		t.Fatal("open branches share mutable property schemas")
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
