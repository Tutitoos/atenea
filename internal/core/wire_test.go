package core_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/core"
)

// The light travels as a word. A number would pin its meaning to the order the
// constants happen to be declared in, and inserting a state between two of them
// would silently reinterpret every reading a consumer had already mapped --
// which is the one mistake a status light can make that nobody can see, since
// every color is plausible.
func TestLightTravelsAsItsName(t *testing.T) {
	for light, want := range map[core.Light]string{
		core.LightGreen: `"green"`,
		core.LightAmber: `"amber"`,
		core.LightRed:   `"red"`,
	} {
		got, err := json.Marshal(light)
		if err != nil {
			t.Fatalf("Marshal(%v): %v", light, err)
		}
		if string(got) != want {
			t.Errorf("Marshal(%v) = %s, want %s", light, got, want)
		}
	}
}

// Unlike String, which answers "red" for anything it does not know because a
// screen should draw the worst case rather than refuse, the encoder refuses. A
// name it invented would be a color on somebody else's screen that no health
// check produced. Whoever adds a fourth light meets this here.
func TestAnUnnamedLightIsRefusedByTheEncoderAndDrawnAsRed(t *testing.T) {
	unnamed := core.Light(9)
	if _, err := json.Marshal(unnamed); err == nil {
		t.Error("Marshal(Light(9)) = no error, want a refusal: an unnamed light must not reach a consumer")
	} else if !strings.Contains(err.Error(), "lightNames") {
		t.Errorf("error = %v, want it to name the table to edit", err)
	}
	if got := unnamed.String(); got != "red" {
		t.Errorf("Light(9).String() = %q, want %q: a screen draws the worst case", got, "red")
	}
}

// One machine runs one binary, but an upgrade replaces it while a service from
// the previous version still holds the socket -- so a command and the service it
// asks can disagree about this encoding until somebody restarts. Asked returns
// false on any decode error, so a strict reader would report "no service" for
// that whole window and the screen would drop to the command's own view.
func TestLightIsReadFromBothANameAndANumber(t *testing.T) {
	for _, tc := range []struct {
		payload string
		want    core.Light
	}{
		{`"green"`, core.LightGreen},
		{`"amber"`, core.LightAmber},
		{`"red"`, core.LightRed},
		{`0`, core.LightGreen},
		{`1`, core.LightAmber},
		{`2`, core.LightRed},
	} {
		var got core.Light
		if err := json.Unmarshal([]byte(tc.payload), &got); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tc.payload, err)
		}
		if got != tc.want {
			t.Errorf("Unmarshal(%s) = %v, want %v", tc.payload, got, tc.want)
		}
	}
}

// A light that cannot be understood is not green. Every one of these would
// otherwise land on the zero value, which is the best color there is.
func TestAnUnreadableLightIsAnErrorAndNotGreen(t *testing.T) {
	for _, payload := range []string{`"blue"`, `""`, `3`, `-1`, `1.5`, `true`, `null`, `{}`} {
		got := core.LightRed
		if err := json.Unmarshal([]byte(payload), &got); err == nil {
			t.Errorf("Unmarshal(%s) = %v, want an error", payload, got)
			continue
		}
		if got == core.LightGreen {
			t.Errorf("Unmarshal(%s) left the light green after failing", payload)
		}
	}
}

// A round trip is what a consumer actually performs, and the status screen
// carries four lights at three depths: the overall one, the orchestrator's, one
// per implementation and one per process. All four are the same type, so all
// four move together -- this is the test that says so.
func TestEveryLightOnTheScreenSurvivesARoundTrip(t *testing.T) {
	atenea := build(t, catalog)

	encoded, err := json.Marshal(atenea.Status())
	if err != nil {
		t.Fatalf("Marshal(Status): %v", err)
	}
	if strings.Contains(string(encoded), `"Light":0`) || strings.Contains(string(encoded), `"Light":1`) {
		t.Error("a light is still traveling as a number")
	}

	var back core.Status
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatalf("Unmarshal(Status): %v", err)
	}
	if back.Light != atenea.Status().Light {
		t.Errorf("overall light = %v, want %v", back.Light, atenea.Status().Light)
	}
	if back.Orchestrator.Light != atenea.Status().Orchestrator.Light {
		t.Errorf("orchestrator light = %v, want %v", back.Orchestrator.Light, atenea.Status().Orchestrator.Light)
	}
	for i, capability := range back.Capabilities {
		for j, impl := range capability.Implementations {
			if want := atenea.Status().Capabilities[i].Implementations[j].Light; impl.Light != want {
				t.Errorf("%s light = %v, want %v", impl.ID, impl.Light, want)
			}
		}
	}
}

// The three keys a consumer outside this repository reads, and only those
// three. The payload carries eighteen and none of the rest is promised: this is
// not a wire contract, it is a note that these three names have a reader who
// cannot see them change. Renaming one is a decision, and it should be made
// here rather than discovered on somebody's status line.
func TestTheThreeKeysAConsumerReadsAreNamedHere(t *testing.T) {
	atenea := build(t, catalog)

	encoded, err := json.Marshal(atenea.Status())
	if err != nil {
		t.Fatalf("Marshal(Status): %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("Unmarshal into a map: %v", err)
	}

	for _, key := range []string{"Light", "Version", "Incidents"} {
		if _, ok := payload[key]; !ok {
			t.Errorf("key %q is gone from the status payload; a reader outside this repo depends on it", key)
		}
	}
	incidents, ok := payload["Incidents"].(map[string]any)
	if !ok {
		t.Fatalf("Incidents = %T, want an object", payload["Incidents"])
	}
	if _, ok := incidents["Unread"]; !ok {
		t.Error(`key "Incidents.Unread" is gone; a reader outside this repo depends on it`)
	}
}
