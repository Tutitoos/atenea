package codebasememory

import (
	"slices"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestReadCallsAskParsesAValidPayload(t *testing.T) {
	got, err := readCallsAsk(map[string]any{
		"file":            "internal/core/core.go",
		"line":            120,
		"column":          7,
		"direction":       "incoming",
		"scope":           []any{"internal", "pkg"},
		"depth":           4,
		"include_snippet": true,
		"snippet_lines":   8,
	})
	if err != nil {
		t.Fatalf("readCallsAsk: %v", err)
	}
	want := callsAsk{
		file: "internal/core/core.go", line: 120, column: 7,
		direction: "incoming", scope: []string{"internal", "pkg"},
		depth: 4, snippet: true, lines: 8,
	}
	if got.file != want.file || got.line != want.line || got.column != want.column ||
		got.direction != want.direction || !slices.Equal(got.scope, want.scope) ||
		got.depth != want.depth || got.snippet != want.snippet || got.lines != want.lines {
		t.Errorf("readCallsAsk = %+v, want %+v", got, want)
	}
}

func TestReadCallsAskAppliesDefaults(t *testing.T) {
	got, err := readCallsAsk(map[string]any{
		"file": "main.go", "line": 1, "column": 1, "direction": "both",
	})
	if err != nil {
		t.Fatalf("readCallsAsk: %v", err)
	}
	if got.depth != defaultDepth {
		t.Errorf("depth = %d, want the default %d", got.depth, defaultDepth)
	}
	if got.lines != defaultSnippetLines {
		t.Errorf("lines = %d, want the default %d", got.lines, defaultSnippetLines)
	}
	if got.snippet {
		t.Error("snippet = true, want false when include_snippet was never asked for")
	}
	if got.scope != nil {
		t.Errorf("scope = %v, want nil when never asked for", got.scope)
	}
}

func TestReadCallsAskNormalizesDirectionCase(t *testing.T) {
	for _, in := range []string{"Incoming", "OUTGOING", " both "} {
		got, err := readCallsAsk(map[string]any{
			"file": "main.go", "line": 1, "column": 1, "direction": in,
		})
		if err != nil {
			t.Fatalf("readCallsAsk(%q): %v", in, err)
		}
		if got.direction != "incoming" && got.direction != "outgoing" && got.direction != "both" {
			t.Errorf("direction %q did not normalize to a known value, got %q", in, got.direction)
		}
	}
}

func TestReadCallsAskRejectsInvalidPayloads(t *testing.T) {
	valid := map[string]any{"file": "main.go", "line": 1, "column": 1, "direction": "both"}
	withOverride := func(key string, value any) map[string]any {
		out := make(map[string]any, len(valid))
		for k, v := range valid {
			out[k] = v
		}
		if value == nil {
			delete(out, key)
		} else {
			out[key] = value
		}
		return out
	}
	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"file is missing", withOverride("file", nil)},
		{"file is blank", withOverride("file", "   ")},
		{"line is missing", withOverride("line", nil)},
		{"line is zero", withOverride("line", 0)},
		{"line is negative", withOverride("line", -1)},
		{"column is missing", withOverride("column", nil)},
		{"column is zero", withOverride("column", 0)},
		{"direction is missing", withOverride("direction", nil)},
		{"direction is a made-up value", withOverride("direction", "sideways")},
		{"depth is zero", withOverride("depth", 0)},
		{"depth is negative", withOverride("depth", -2)},
		{"snippet_lines is zero", withOverride("snippet_lines", 0)},
		{"snippet_lines is negative", withOverride("snippet_lines", -5)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readCallsAsk(tc.payload)
			if got := contract.KindOf(err); got != contract.FailureInvalidInput {
				t.Errorf("kind = %v, want invalid_input (err = %v)", got, err)
			}
		})
	}
}
