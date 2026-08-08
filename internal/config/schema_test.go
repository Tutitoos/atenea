package config_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/Tutitoos/atenea/internal/config"
	"github.com/Tutitoos/atenea/pkg/contract"
)

// A capability that cannot describe itself cannot be advertised: a client
// reading tools/list, or a model being asked to fill in a form, gets nothing to
// act on. The generator refuses a field type it cannot express rather than
// quietly omitting the field, so this walks the whole shipped catalog to prove
// no capability Atenea ships is in that state -- in either direction, because
// a capability nobody can call and a capability nobody can read the answer of
// are the same dead end.
func TestEveryShippedCapabilityCanDescribeItself(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	if len(cfg.Capabilities) == 0 {
		t.Fatal("the shipped catalog is empty, so this proves nothing")
	}

	for _, capability := range cfg.Capabilities {
		t.Run(capability.ID, func(t *testing.T) {
			for _, side := range []struct {
				name   string
				fields []contract.Field
				build  func() (map[string]any, error)
			}{
				{"input", capability.Inputs, capability.InputSchema},
				{"output", capability.Outputs, capability.OutputSchema},
			} {
				schema, err := side.build()
				if err != nil {
					t.Fatalf("%s schema: %v", side.name, err)
				}
				describes(t, side.name, schema, side.fields)
				if _, err := json.Marshal(schema); err != nil {
					t.Errorf("%s schema does not marshal: %v", side.name, err)
				}
			}
		})
	}
}

// describes asserts one object node states a usable shape, then follows every
// nested record. The depth matters: checkPayload recurses into records and
// refuses unknown keys there too, so a schema that only closes its root is
// already out of step with the validator two lines further in.
func describes(t *testing.T, side string, node map[string]any, fields []contract.Field) {
	t.Helper()

	if node["type"] != "object" {
		t.Errorf("%s: node is %v, want an object", side, node["type"])
	}
	closed, stated := node["additionalProperties"].(bool)
	if !stated {
		t.Errorf("%s: nothing is said about unknown keys, and the validator refuses them", side)
	} else if closed {
		t.Errorf("%s: unknown keys are advertised as welcome, and the validator refuses them", side)
	}

	// Required is a promise in both directions: a field the schema demands but
	// the capability does not is a caller sending what nobody asked for, and
	// the reverse is a caller refused for an omission it was never warned of.
	promised, _ := node["required"].([]string)
	declared := make([]string, 0, len(fields))
	for _, field := range fields {
		if field.Required {
			declared = append(declared, field.Name)
		}
	}
	if !slices.Equal(promised, declared) {
		t.Errorf("%s: schema requires %v, capability requires %v", side, promised, declared)
	}

	properties, _ := node["properties"].(map[string]any)
	if len(properties) != len(fields) {
		t.Errorf("%s: %d properties for %d declared fields", side, len(properties), len(fields))
	}
	for _, field := range fields {
		entry, described := properties[field.Name].(map[string]any)
		if !described {
			t.Errorf("%s: field %q is %v, want a schema node",
				side, field.Name, properties[field.Name])
			continue
		}
		if entry["type"] == nil {
			t.Errorf("%s: field %q states no type", side, field.Name)
		}
		switch field.Type {
		case contract.TypeRecord:
			describes(t, side+" "+field.Name, entry, field.Fields)
		case contract.TypeRecordList:
			items, ok := entry["items"].(map[string]any)
			if !ok {
				t.Errorf("%s: field %q describes no element", side, field.Name)
				continue
			}
			describes(t, side+" "+field.Name+"[]", items, field.Fields)
		}
	}
}

// The schema says what will be accepted and ValidateInput is what accepts, so
// every capability Atenea ships is walked through both. A payload built to
// satisfy the schema and then refused by the validator is a caller punished for
// doing as it was told, and it is the failure a client cannot diagnose: it did
// exactly what the tool list advertised.
//
// The payload is synthesized from the SCHEMA's own promises, never from the
// fields, so the validator is the one judging and the schema is the one on
// trial.
func TestEveryShippedSchemaAgreesWithItsValidator(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}

	for _, capability := range cfg.Capabilities {
		t.Run(capability.ID, func(t *testing.T) {
			schema, err := capability.InputSchema()
			if err != nil {
				t.Fatalf("InputSchema: %v", err)
			}

			valid := satisfy(t, schema)
			if err := capability.ValidateInput(valid); err != nil {
				t.Errorf("the validator refused what the schema promises: %v", err)
			}

			unknown := map[string]any{"definitely_not_declared": true}
			for name, value := range valid {
				unknown[name] = value
			}
			if err := capability.ValidateInput(unknown); err == nil {
				t.Error("the validator accepted a key the schema declares closed")
			}
		})
	}
}

// satisfy builds a payload the schema promises to accept: every property it
// declares, filled by the type it states for that property.
//
// Every property and not only the required ones, because the optional fields
// are where a type can be misdescribed without anything noticing -- a payload
// that skips them proves only that the required ones are right.
func satisfy(t *testing.T, node map[string]any) map[string]any {
	t.Helper()

	out := map[string]any{}
	properties, _ := node["properties"].(map[string]any)
	for name, described := range properties {
		entry, ok := described.(map[string]any)
		if !ok {
			t.Fatalf("property %q is %v, not a schema node", name, described)
		}
		out[name] = value(t, entry)
	}
	return out
}

func value(t *testing.T, node map[string]any) any {
	t.Helper()

	// A closed set means only its own words are acceptable, so an invented
	// string would be refused for the wrong reason.
	if set, ok := node["enum"].([]string); ok && len(set) > 0 {
		return set[0]
	}
	switch node["type"] {
	case "string":
		return "x"
	case "integer":
		return 1
	case "boolean":
		return true
	case "array":
		element, ok := node["items"].(map[string]any)
		if !ok {
			return []any{}
		}
		return []any{value(t, element)}
	case "object":
		return satisfy(t, node)
	default:
		t.Fatalf("the schema states a type nothing can satisfy: %v", node["type"])
		return nil
	}
}
