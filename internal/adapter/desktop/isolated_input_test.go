package desktop_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/desktop"
	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/internal/mcpstdio"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestLegacyOutputSchemaRefusesBeforeHelperContact(t *testing.T) {
	runner, err := desktop.New(desktop.Options{
		Responsible:     func() bool { return true },
		SignatureStable: func() (bool, string) { return true, "" },
		Session: func(context.Context) (*mcpstdio.Session, error) {
			t.Fatal("legacy configuration reached the helper")
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, variant := range []string{"missing receipt", "partial receipt", "fractional field typed as int", "restricted enum", "required virtual pointer", "extra required field"} {
		t.Run(variant, func(t *testing.T) {
			req := isolatedRequest(t)
			outputs := req.Capability.Outputs
			idx := slices.IndexFunc(outputs, func(field contract.Field) bool { return field.Name == "input" })
			switch variant {
			case "missing receipt":
				outputs = slices.Delete(outputs, idx, idx+1)
			case "partial receipt":
				outputs[idx].Fields = outputs[idx].Fields[:2]
			case "fractional field typed as int":
				for i := range outputs[idx].Fields {
					if outputs[idx].Fields[i].Name == "elapsed_ms" {
						outputs[idx].Fields[i].Type = contract.TypeInt
					}
				}
			case "restricted enum":
				outputs[idx].Fields[0].Enum = []string{"cg_event_pid"}
			case "required virtual pointer":
				for i := range outputs[idx].Fields {
					if outputs[idx].Fields[i].Name == "virtual_pointer_x" {
						outputs[idx].Fields[i].Required = true
					}
				}
			case "extra required field":
				outputs = append(outputs, contract.Field{Name: "unsupported", Type: contract.TypeString, Required: true})
			}
			req.Capability.Outputs = outputs
			_, err = runner.Run(t.Context(), req)
			if contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), "incompatible private-input output schema") {
				t.Fatalf("incompatible schema not refused before input: %v", err)
			}
		})
	}
}

func TestAllDefaultMutationSchemasPassPreflight(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"click", "move", "drag", "scroll", "type", "key"} {
		t.Run(name, func(t *testing.T) {
			called := false
			runner, err := desktop.New(desktop.Options{
				Responsible: func() bool { return true }, SignatureStable: func() (bool, string) { return true, "" },
				Session: func(context.Context) (*mcpstdio.Session, error) {
					called = true
					return nil, errors.New("preflight passed")
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			idx := slices.IndexFunc(cfg.Capabilities, func(c contract.Capability) bool { return c.ID == "desktop."+name })
			payload := map[string]any{"application": "com.apple.Notes", "frame_id": "frame-1"}
			for _, field := range cfg.Capabilities[idx].Inputs {
				if _, present := payload[field.Name]; !present && field.Required {
					if field.Type == contract.TypeString {
						payload[field.Name] = "a"
					} else {
						payload[field.Name] = 1
					}
				}
			}
			req := request(t, cfg.Capabilities[idx], payload)
			req.Implementation.ID = "macos." + name
			_, err = runner.Run(t.Context(), req)
			if !called || err == nil || !strings.Contains(err.Error(), "preflight passed") {
				t.Fatalf("default %s schema rejected: %v", name, err)
			}
		})
	}
}

func isolatedRequest(t *testing.T) contract.RunRequest {
	t.Helper()
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(cfg.Capabilities, func(c contract.Capability) bool { return c.ID == "desktop.click" })
	req := request(t, cfg.Capabilities[i], map[string]any{"application": "com.apple.Notes", "frame_id": "frame-1", "x": 3, "y": 4})
	req.Implementation.ID = "macos.click"
	return req
}

func privateReceipt() map[string]any {
	return map[string]any{"action_sent": true, "frame_id": "frame-1", "window_id": 9,
		"dominant_display_id": 1, "geometry_generation": "g1", "input_backend": "cg_event_pid", "input_source": "private",
		"target_pid": 42, "events_sent": 2, "pointer_before": []float64{5, 6}, "pointer_after": []float64{5, 6},
		"frontmost_before": 77, "frontmost_after": 77, "elapsed_ms": 23.5, "virtual_pointer": []float64{30, 40}}
}

func TestPrivateInputRequiresMonitorAndNewHelperEvenWithoutVisuals(t *testing.T) {
	for _, tc := range []struct {
		name              string
		isolated, monitor bool
	}{
		{"legacy helper", false, true}, {"disabled monitor", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := fakeHelper(t, map[string]any{"health": map[string]any{"accessibility": true, "screen_recording": true,
				"isolated_input": tc.isolated, "input_monitor_active": tc.monitor}})
			runner, err := desktop.New(desktop.Options{Session: session, Responsible: func() bool { return true }, Allowed: []string{"com.apple.Notes"}, VisualFeedback: false})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runner.Run(t.Context(), isolatedRequest(t))
			if contract.KindOf(err) != contract.FailurePermissionDenied {
				t.Fatalf("unsafe helper accepted: %v", err)
			}
			for len(callsSeen) > 0 {
				call := <-callsSeen
				if call["name"] != "health" {
					t.Fatalf("input reached unsafe helper: %v", call["name"])
				}
			}
		})
	}
}

func TestPrivateReceiptIsBoundToResolvedPIDAndReportsBackend(t *testing.T) {
	for _, tc := range []struct {
		name     string
		patch    map[string]any
		rejected bool
	}{
		{"valid", nil, false},
		{"semantic button", map[string]any{"input_backend": "ax_press", "events_sent": 1}, false},
		{"semantic selection", map[string]any{"input_backend": "ax_selection", "events_sent": 1}, false},
		{"global source", map[string]any{"input_source": "combined"}, true},
		{"global backend", map[string]any{"input_backend": "hid"}, true},
		{"wrong process", map[string]any{"target_pid": 77}, true},
		{"no events", map[string]any{"events_sent": 0}, true},
		{"missing pointer sample", map[string]any{"pointer_after": nil}, true},
		{"null coordinate", map[string]any{"pointer_after": []any{nil, nil}}, true},
		{"null focus", map[string]any{"frontmost_before": nil}, true},
		{"null elapsed", map[string]any{"elapsed_ms": nil}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply := privateReceipt()
			maps.Copy(reply, tc.patch)
			session := fakeHelper(t, map[string]any{"health": map[string]any{"accessibility": true, "screen_recording": true, "isolated_input": true, "input_monitor_active": true},
				"list_apps": map[string]any{"apps": []any{map[string]any{"pid": 42, "name": "Notes", "bundle_id": "com.apple.Notes"}}}, "click": reply})
			runner, err := desktop.New(desktop.Options{Session: session, Responsible: func() bool { return true }, Allowed: []string{"com.apple.Notes"}})
			if err != nil {
				t.Fatal(err)
			}
			result, err := runner.Run(t.Context(), isolatedRequest(t))
			if tc.rejected {
				if contract.KindOf(err) != contract.FailureUnavailable {
					t.Fatalf("receipt accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := isolatedRequest(t).Capability.ValidateOutput(result.Result); err != nil {
				t.Fatalf("real receipt shape diverged from the output schema: %v", err)
			}
			input := result.Result["input"].(map[string]any)
			if input["backend"] != reply["input_backend"] || input["target_pid"] != 42 || input["pointer_unchanged"] != true || input["frontmost_after"] != 77 || input["virtual_pointer_x"] != float64(30) || input["virtual_pointer_y"] != float64(40) {
				t.Fatalf("bad provenance: %v", input)
			}
		})
	}
}
