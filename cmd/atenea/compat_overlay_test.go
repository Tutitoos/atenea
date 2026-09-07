package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCompatOverlayEmitsLowercaseMachineContract(t *testing.T) {
	var out bytes.Buffer
	if err := cmdCompatOverlay([]string{"--client", "codex"}, &out); err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["args"] == nil {
		t.Fatalf("missing lowercase args: %s", out.String())
	}
	if payload["Args"] != nil || payload["Env"] != nil {
		t.Fatalf("exported Go field names leaked into JSON: %s", out.String())
	}
}
