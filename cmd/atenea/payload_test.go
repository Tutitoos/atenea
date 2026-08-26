package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// extractShaped is a capability whose required input is a record_list, which
// is the shape --set refuses and the reason --payload exists. web.extract is
// the real one; this is its skeleton.
func extractShaped() contract.Capability {
	return contract.Capability{
		ID: "web.extract", Version: contract.Version{Major: 1},
		Summary: "Pull named fields out of one web page.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectExternal},
		Inputs: []contract.Field{
			{Name: "url", Type: contract.TypeString, Required: true, Summary: "The page."},
			{Name: "fields", Type: contract.TypeRecordList, Required: true, Summary: "The selectors.",
				Fields: []contract.Field{
					{Name: "name", Type: contract.TypeString, Required: true, Summary: "What to call it."},
					{Name: "selector", Type: contract.TypeString, Required: true, Summary: "How to find it."},
				}},
		},
	}
}

func payloadFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "payload.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// The whole point: a capability whose required input is a record_list was
// reachable from every MCP client and from nothing on the command line.
func TestAPayloadFileCarriesTheShapeSetCannotExpress(t *testing.T) {
	path := payloadFile(t, `{
		"url": "https://example.com/",
		"fields": [{"name": "title", "selector": ".t"}, {"name": "price", "selector": ".p"}]
	}`)
	payload, err := askPayload(extractShaped(), nil, path)
	if err != nil {
		t.Fatalf("askPayload: %v", err)
	}
	// Nothing types it here on purpose -- the capability's own declaration
	// does, the same way it does for --set.
	if err := extractShaped().ValidateInput(payload); err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
	fields, ok := payload["fields"].([]any)
	if !ok || len(fields) != 2 {
		t.Fatalf("fields = %#v, want the two records the file named", payload["fields"])
	}
}

// Two ways to say the same thing in one invocation is a rule about which wins
// per field, and nobody would remember it.
func TestPayloadAndSetAreMutuallyExclusive(t *testing.T) {
	path := payloadFile(t, `{"url": "https://example.com/"}`)
	_, err := askPayload(extractShaped(), fieldList{"url=https://other.example/"}, path)
	if err == nil {
		t.Fatal("both were accepted")
	}
	if !strings.Contains(err.Error(), "cannot both be given") {
		t.Errorf("error = %v, want it to say why", err)
	}
}

// Without --payload nothing changes: --set is still the whole story, including
// its refusal of a record.
func TestWithoutAPayloadFileSetStillDecides(t *testing.T) {
	payload, err := askPayload(extractShaped(), fieldList{"url=https://example.com/"}, "")
	if err != nil {
		t.Fatalf("askPayload: %v", err)
	}
	if payload["url"] != "https://example.com/" {
		t.Errorf("payload = %#v", payload)
	}
	// And the refusal now names the way out rather than only the wall.
	_, err = askPayload(extractShaped(), fieldList{"fields=whatever"}, "")
	if err == nil {
		t.Fatal("--set accepted a record_list")
	}
	if !strings.Contains(err.Error(), "--payload") {
		t.Errorf("error = %v, want it to point at --payload", err)
	}
}

func TestAPayloadFileIsRefusedWhenItIsNotOne(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"not json", "no soy json", "not a json object"},
		{"a bare list", `[1, 2]`, "not a json object"},
		// `null` decodes without error into a nil map, and a nil payload
		// reaching ValidateInput reads as "no fields given" rather than as the
		// malformed file it is.
		{"null", `null`, "is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := askPayload(extractShaped(), nil, payloadFile(t, tc.body))
			if err == nil {
				t.Fatal("a malformed payload was accepted")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestAMissingPayloadFileSaysWhichOne(t *testing.T) {
	_, err := askPayload(extractShaped(), nil, "/definitely/not/here.json")
	if err == nil {
		t.Fatal("a missing file was accepted")
	}
	if !strings.Contains(err.Error(), "/definitely/not/here.json") {
		t.Errorf("error = %v, want it to name the file", err)
	}
}

// The help has to name it, or it is a flag only its author knows about.
func TestTheHelpNamesThePayloadFlag(t *testing.T) {
	text, ok := commandHelp["ask"]
	if !ok {
		t.Fatal("ask has no usage text")
	}
	if !strings.Contains(text, "--payload FILE") {
		t.Error("ask's help does not mention --payload")
	}
}
