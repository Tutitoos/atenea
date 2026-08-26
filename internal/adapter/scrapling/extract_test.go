package scrapling_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/scrapling"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func extractCapability() contract.Capability {
	return contract.Capability{
		ID: scrapling.CapabilityExtract, Version: contract.Version{Major: 1},
		Summary: "Pull named fields out of one web page.",
		Effects: []contract.Effect{contract.EffectRead, contract.EffectExternal},
		Inputs: []contract.Field{
			{Name: "url", Type: contract.TypeString, Required: true, Summary: "The page to read."},
			{Name: "fields", Type: contract.TypeRecordList, Required: true,
				Summary: "The named selectors to read.",
				Fields: []contract.Field{
					{Name: "name", Type: contract.TypeString, Required: true, Summary: "What to call it."},
					{Name: "selector", Type: contract.TypeString, Required: true, Summary: "How to find it."},
				}},
			{Name: "format", Type: contract.TypeString, Summary: "How to render each match.",
				Enum: []string{"text", "markdown", "html"}},
		},
		Outputs: []contract.Field{{Name: "rows", Type: contract.TypeRecordList, Required: true,
			Summary: "One row per match.",
			Fields: []contract.Field{
				{Name: "field", Type: contract.TypeString, Required: true, Summary: "The name given."},
				{Name: "index", Type: contract.TypeInt, Required: true, Summary: "Which match."},
				{Name: "value", Type: contract.TypeString, Required: true, Summary: "The match."},
			}}},
	}
}

func extractRequest(t *testing.T, implementation string, payload map[string]any) contract.RunRequest {
	t.Helper()
	capability := extractCapability()
	return contract.RunRequest{
		Capability:     capability,
		Implementation: contract.Implementation{ID: implementation, Capability: capability.ID},
		Repository:     contract.Repository{ID: "work", Path: t.TempDir()},
		Payload:        payload,
		Permission:     contract.Permission{Task: "read named fields", Effects: capability.Effects},
	}
}

func fields(pairs ...string) []any {
	out := make([]any, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, map[string]any{"name": pairs[i], "selector": pairs[i+1]})
	}
	return out
}

// bySelector answers each call with whatever that selector was told to match,
// which is how the far side behaves: one call, one selector, one match list.
func bySelector(matches map[string][]string) func(map[string]any) any {
	return func(args map[string]any) any {
		selector, _ := args["css_selector"].(string)
		parts := make([]any, 0, len(matches[selector])+1)
		for _, m := range matches[selector] {
			parts = append(parts, m)
		}
		// The trailing empty the far side always sends.
		parts = append(parts, "")
		return map[string]any{"status": 200, "url": "https://shop.example/", "content": parts}
	}
}

func rowsOf(t *testing.T, out contract.Outcome) []map[string]any {
	t.Helper()
	rows, ok := out.Result["rows"].([]map[string]any)
	if !ok {
		t.Fatalf("result = %+v", out.Result)
	}
	return rows
}

// The shape the capability promises: one row per match, keyed by the caller's
// own name for the selector, with the index restarting per field.
func TestEachMatchBecomesItsOwnRow(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": bySelector(map[string][]string{
			".title": {"Widget", "Gadget", "Doohickey"},
			".price": {"9.99", "19.99", "29.99"},
		}),
	})})
	out, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{
			"url":    "https://shop.example/",
			"fields": fields("title", ".title", "price", ".price"),
		}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := rowsOf(t, out)
	if len(rows) != 6 {
		t.Fatalf("got %d rows, want 6", len(rows))
	}
	// Grouped by field, in the order the caller asked, and indexed from zero
	// within each field rather than across the whole answer.
	want := []struct {
		field string
		index int
		value string
	}{
		{"title", 0, "Widget"}, {"title", 1, "Gadget"}, {"title", 2, "Doohickey"},
		{"price", 0, "9.99"}, {"price", 1, "19.99"}, {"price", 2, "29.99"},
	}
	for i, w := range want {
		if rows[i]["field"] != w.field || rows[i]["index"] != w.index || rows[i]["value"] != w.value {
			t.Errorf("row %d = %+v, want %+v", i, rows[i], w)
		}
	}
	if err := extractCapability().ValidateOutput(out.Result); err != nil {
		t.Errorf("ValidateOutput: %v", err)
	}
	if out.SpentUSD != 0 || !out.SpentUSDKnown {
		t.Errorf("spent = %v known = %v, want a declared zero", out.SpentUSD, out.SpentUSDKnown)
	}
}

// One call per field, each carrying that field's selector and nothing else of
// the caller's. The whole cost model of this capability rests on the count.
func TestOneCallPerFieldCarryingThatSelector(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": bySelector(map[string][]string{".a": {"1"}, ".b": {"2"}, ".c": {"3"}}),
	})})
	_, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{
			"url":    "https://shop.example/",
			"fields": fields("a", ".a", "b", ".b", "c", ".c"),
			"format": "html",
		}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	seen := make([]string, 0, 3)
	for range 3 {
		params := <-callsSeen
		args, _ := params["arguments"].(map[string]any)
		seen = append(seen, args["css_selector"].(string))
		if args["url"] != "https://shop.example/" {
			t.Errorf("url = %v", args["url"])
		}
		// The rendering is chosen on the way in and applies to every field.
		if args["extraction_type"] != "html" {
			t.Errorf("extraction_type = %v, want html on every call", args["extraction_type"])
		}
		if len(args) != 3 {
			t.Errorf("args = %+v, want exactly the three this adapter builds", args)
		}
	}
	if strings.Join(seen, ",") != ".a,.b,.c" {
		t.Errorf("selectors asked = %v, want each field's own in order", seen)
	}
}

// A field that matched nothing contributes no rows. Not a row with an empty
// value: the page had none, and inventing a blank would be this adapter
// answering a question the page did not.
func TestAFieldThatMatchedNothingContributesNoRows(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": bySelector(map[string][]string{".here": {"found"}, ".gone": nil}),
	})})
	out, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{
			"url":    "https://shop.example/",
			"fields": fields("here", ".here", "gone", ".gone"),
		}))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	rows := rowsOf(t, out)
	if len(rows) != 1 || rows[0]["field"] != "here" {
		t.Fatalf("rows = %+v, want only the field that matched", rows)
	}
}

// A challenge on any field stops the whole extraction. A record built partly
// out of a challenge page, handed back as a success, is worse than no record.
func TestAChallengeOnOneFieldStopsTheWholeExtraction(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": func(args map[string]any) any {
			if args["css_selector"] == ".second" {
				return map[string]any{"status": 200, "content": []any{"Just a moment...", ""}}
			}
			return map[string]any{"status": 200, "content": []any{"fine", ""}}
		},
	})})
	_, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{
			"url":    "https://shop.example/",
			"fields": fields("first", ".first", "second", ".second", "third", ".third"),
		}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailureUnavailable {
		t.Fatalf("error = %v, want unavailable so the funnel escalates", err)
	}
	// The message names which field was being read, because "the page was
	// blocked" is less useful than knowing where it stopped.
	if !strings.Contains(err.Error(), "second") {
		t.Errorf("error = %v, want it to name the field it stopped on", err)
	}
}

// The last level does not escalate, here as in web.fetch: there is nobody to
// fall back to, so the challenge is reported as what came back.
func TestExtractStealthReportsABlockAsTheAnswer(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"stealthy_fetch": bySelector(map[string][]string{".x": {"Just a moment..."}}),
	})})
	out, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractStealth,
		map[string]any{"url": "https://shop.example/", "fields": fields("x", ".x")}))
	if err != nil {
		t.Fatalf("the last level escalated instead of answering: %v", err)
	}
	if rows := rowsOf(t, out); len(rows) != 1 {
		t.Errorf("rows = %+v", rows)
	}
}

// The rows are keyed by name, so two fields sharing one produce an answer
// nobody can pivot. Refused before anything is fetched.
func TestTwoFieldsCannotShareAName(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": bySelector(map[string][]string{".a": {"1"}}),
	})})
	_, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{
			"url":    "https://shop.example/",
			"fields": fields("price", ".a", "price", ".b"),
		}))
	if err == nil {
		t.Fatal("a duplicate field name was accepted")
	}
	if !strings.Contains(err.Error(), "twice") {
		t.Errorf("error = %v, want it to say what was wrong", err)
	}
}

func TestExtractRefusesAnEmptyOrMalformedFieldList(t *testing.T) {
	cases := []struct {
		name    string
		fields  any
		wantErr string
	}{
		{"no fields at all", []any{}, "at least one field"},
		{"a field with no selector", []any{map[string]any{"name": "x", "selector": ""}}, "name and a selector"},
		{"a field with no name", []any{map[string]any{"name": "", "selector": ".x"}}, "name and a selector"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
				"make_request": bySelector(nil),
			})})
			_, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
				map[string]any{"url": "https://shop.example/", "fields": tc.fields}))
			if err == nil {
				t.Fatal("a malformed field list was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The gate is the same gate, and it runs before any field is read rather than
// once per field: the url does not change between them, and a second
// resolution could disagree with the first and leave half the fields fetched
// under a verdict the other half never got.
func TestTheGateRunsOnceAndBeforeAnyField(t *testing.T) {
	resolutions := 0
	runner := newRunner(t, scrapling.Options{
		Session: fakeServer(t, map[string]any{
			"make_request": bySelector(map[string][]string{".a": {"1"}, ".b": {"2"}}),
		}),
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			resolutions++
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
	})
	if _, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{
			"url":    "https://shop.example/",
			"fields": fields("a", ".a", "b", ".b"),
		})); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if resolutions != 1 {
		t.Errorf("resolved %d times, want once for the whole extraction", resolutions)
	}
}

func TestExtractRefusesADeniedDestination(t *testing.T) {
	runner := newRunner(t, scrapling.Options{Session: fakeServer(t, map[string]any{
		"make_request": bySelector(map[string][]string{".a": {"1"}}),
	})})
	_, err := runner.Run(t.Context(), extractRequest(t, scrapling.ImplementationExtractRequest,
		map[string]any{"url": "http://127.0.0.1:40010/mcp", "fields": fields("a", ".a")}))
	var failure *contract.Failure
	if !errors.As(err, &failure) || failure.Kind != contract.FailurePermissionDenied {
		t.Fatalf("error = %v, want permission_denied", err)
	}
}
