package contract

import "testing"

func TestStructuralPartialRecognizesEquivalentMarkers(t *testing.T) {
	for name, value := range map[string]any{
		"truncated bool":      map[string]any{"truncated": true},
		"truncated string":    map[string]any{"truncated": "true"},
		"trimmed count":       map[string]any{"source_trimmed": 1},
		"nested partial":      map[string]any{"rows": []any{map[string]any{"partial": "yes"}}},
		"continuation cursor": map[string]any{"next_cursor": "next"},
	} {
		if !StructuralPartial(value) {
			t.Errorf("%s was not recognized as partial", name)
		}
	}
	for name, value := range map[string]any{
		"false":        map[string]any{"truncated": false},
		"zero":         map[string]any{"source_trimmed": 0},
		"empty cursor": map[string]any{"next_cursor": ""},
	} {
		if StructuralPartial(value) {
			t.Errorf("%s was incorrectly recognized as partial", name)
		}
	}
}
