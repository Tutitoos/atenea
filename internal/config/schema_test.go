package config_test

import (
	"encoding/json"
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

// The schema says what will be accepted and ValidateInput is what accepts. For
// the one capability every client calls first, that agreement is worth
// asserting against the shipped declaration rather than a fixture: a payload
// the schema advertises and the validator then refuses is a caller punished
// for doing as it was told.
func TestTheShippedSearchSchemaAgreesWithItsValidator(t *testing.T) {
	cfg, err := config.Defaults()
	if err != nil {
		t.Fatalf("Defaults: %v", err)
	}
	var search contract.Capability
	for _, capability := range cfg.Capabilities {
		if capability.ID == "code.search" {
			search = capability
		}
	}
	if search.ID == "" {
		t.Fatal("the shipped catalog has no code.search")
	}
	schema, err := search.InputSchema()
	if err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	properties, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]string)

	valid := map[string]any{}
	for _, name := range required {
		valid[name] = "x"
	}
	if err := search.ValidateInput(valid); err != nil {
		t.Errorf("the schema's own required set was refused by the validator: %v", err)
	}

	unknown := map[string]any{"definitely_not_declared": true}
	for name := range valid {
		unknown[name] = valid[name]
	}
	if _, declared := properties["definitely_not_declared"]; declared {
		t.Fatal("the fixture key is declared, so this proves nothing")
	}
	if err := search.ValidateInput(unknown); err == nil {
		t.Error("the validator accepted a key the schema does not declare")
	}
}
