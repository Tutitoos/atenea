package core

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Tutitoos/atenea/internal/registry"
	"github.com/Tutitoos/atenea/pkg/contract"
)

func TestAimedAtIsIdempotentWithoutMutatingItsInput(t *testing.T) {
	catalog := registry.New()
	for _, id := range []string{"first", "second"} {
		if err := catalog.AddRepository(contract.Repository{ID: id, Path: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	v := &conversation{core: &Core{catalog: catalog}}
	for _, tc := range []struct {
		name     string
		required any
		want     []string
	}{
		{"absent", nil, []string{"repository"}},
		{"strings", []string{"file"}, []string{"file", "repository"}},
		{"strings already scoped", []string{"file", "repository"}, []string{"file", "repository"}},
		{"decoded", []any{"file"}, []string{"file", "repository"}},
		{"decoded already scoped", []any{"file", "repository"}, []string{"file", "repository"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := map[string]any{
				"type": "object",
				"properties": map[string]any{
					"file": map[string]any{"type": "string"},
				},
			}
			if tc.required != nil {
				schema["required"] = tc.required
			}
			before, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			first := v.aimedAt(schema)
			second := v.aimedAt(first)
			if !reflect.DeepEqual(first, second) {
				t.Errorf("repeated scoping changes the schema: first=%v second=%v", first, second)
			}
			if got := second["required"]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("required = %v, want %v", got, tc.want)
			}
			// The returned maps and required list must not alias the input.
			first["properties"].(map[string]any)["other"] = map[string]any{"type": "string"}
			first["required"].([]string)[0] = "changed"
			after, err := json.Marshal(schema)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Errorf("input schema changed: before=%s after=%s", before, after)
			}
		})
	}
}
