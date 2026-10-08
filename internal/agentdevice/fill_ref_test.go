package agentdevice

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestFillRefLabelContractForPinnedReleases(t *testing.T) {
	for _, version := range []string{Version, CandidateVersion} {
		t.Run(version, func(t *testing.T) {
			upstream, err := schemas.ReadFile("testdata/fill-" + version + ".json")
			if err != nil {
				t.Fatal(err)
			}
			original, fingerprint := bytes.Clone(upstream), Fingerprint(upstream)
			advertised, err := AdvertisedSchema(version, "fill", upstream)
			if err != nil {
				t.Fatal(err)
			}
			var pinned map[string]any
			if err := json.Unmarshal(upstream, &pinned); err != nil {
				t.Fatal(err)
			}
			variants := advertised["properties"].(map[string]any)["target"].(map[string]any)["oneOf"].([]any)
			pinnedVariants := pinned["properties"].(map[string]any)["target"].(map[string]any)["oneOf"].([]any)
			ref := variants[0].(map[string]any)
			if _, present := ref["properties"].(map[string]any)["label"]; present || ref["additionalProperties"] != false {
				t.Fatal("advertised fill ref permits label or unknown fields")
			}
			if pinnedVariants[0].(map[string]any)["properties"].(map[string]any)["label"] == nil {
				t.Fatal("upstream fill ref label was removed")
			}
			// The existing selector pattern is the only selector adaptation.
			pinnedVariants[1].(map[string]any)["properties"].(map[string]any)["selector"].(map[string]any)["pattern"] = `=`
			if !reflect.DeepEqual(variants[1:], pinnedVariants[1:]) {
				t.Fatal("selector or point target contract changed")
			}
			for _, ref := range []string{"@e12", "@e12~s4"} {
				for _, label := range []any{"fixture-private-label", "", nil, false, float64(7)} {
					args := map[string]any{
						"session": "flow", "cwd": "/fixture", "text": "fixture-private-text",
						"target": map[string]any{"kind": "ref", "ref": ref, "label": label},
					}
					before, err := json.Marshal(args)
					if err != nil {
						t.Fatal(err)
					}
					if err := validateSchema(advertised, args, "arguments"); err == nil {
						t.Fatal("advertised fill ref accepted a present label")
					}
					if err := Validate(version, "fill", upstream, args); err == nil || !strings.Contains(err.Error(), "omit target.label") || strings.Contains(err.Error(), "fixture-private-") {
						t.Fatalf("fill label refusal missing hint or leaked a private value: %v", err)
					}
					wireTarget := WireArguments(version, args)["target"].(map[string]any)
					if wireLabel, present := wireTarget["label"]; !present || !reflect.DeepEqual(wireLabel, label) {
						t.Fatal("wire adaptation stripped or changed label")
					}
					after, err := json.Marshal(args)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("validation or wire adaptation changed arguments")
					}
				}
			}
			for _, target := range []map[string]any{
				{"kind": "ref", "ref": "@e12"},
				{"kind": "ref", "ref": "@e12~s4"},
				{"kind": "selector", "selector": "role=textbox"},
				{"kind": "point", "x": float64(1), "y": float64(2)},
			} {
				args := map[string]any{"session": "flow", "cwd": "/fixture", "target": target, "text": ""}
				if err := validateSchema(advertised, args, "arguments"); err != nil {
					t.Fatalf("advertised fill rejected label-free target or empty text: %v", err)
				}
				if err := Validate(version, "fill", upstream, args); err != nil {
					t.Fatalf("runtime fill rejected label-free target or empty text: %v", err)
				}
			}
			adapted, err := json.Marshal(advertised)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(upstream, original) || Fingerprint(upstream) != fingerprint {
				t.Fatal("upstream fill bytes or fingerprint mutated")
			}
			if err := VerifySchema(version, "fill", upstream); err != nil {
				t.Fatal(err)
			}
			if err := VerifySchema(version, "fill", adapted); err == nil {
				t.Fatal("advertised fill schema accepted as upstream provenance")
			}

			click, err := schemas.ReadFile("testdata/click-" + version + ".json")
			if err != nil {
				t.Fatal(err)
			}
			clickOriginal, clickFingerprint := bytes.Clone(click), Fingerprint(click)
			clickAdvertised, err := AdvertisedSchema(version, "click", click)
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"session": "flow", "cwd": "/fixture", "target": map[string]any{"kind": "ref", "ref": "@e12~s4", "label": "fixture-label"}}
			if err := validateSchema(clickAdvertised, args, "arguments"); err != nil {
				t.Fatalf("advertised click rejected ref label: %v", err)
			}
			if err := Validate(version, "click", click, args); err != nil {
				t.Fatalf("runtime click rejected ref label: %v", err)
			}
			if !bytes.Equal(click, clickOriginal) || Fingerprint(click) != clickFingerprint {
				t.Fatal("upstream click bytes or fingerprint mutated")
			}
		})
	}
}
