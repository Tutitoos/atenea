package contract_test

import (
	"fmt"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// A schema is a promise about what will be accepted, and the validator is what
// actually accepts. Two separate walks over the same declaration is exactly the
// shape that drifts, so this pins them: for every payload below, the schema's
// verdict and ValidateInput's verdict have to be the same verdict.
//
// The walker is deliberately dumb and reads only the GENERATED schema, never
// the fields. That is the whole point -- if the generator stops emitting
// additionalProperties, or forgets a required entry, the walker starts
// disagreeing with checkPayload and this test is how anyone finds out.
func TestTheSchemaAndTheValidatorAgree(t *testing.T) {
	capability := searchCapability()

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"exactly what is declared", map[string]any{
			"query": "needle", "case_sensitive": true,
			"paths": []any{"internal"}, "mode": "regex",
		}},
		{"only the required field", map[string]any{"query": "needle"}},
		{"a field nobody promised", map[string]any{
			"query": "needle", "recursive": true,
		}},
		{"the required field missing", map[string]any{"case_sensitive": true}},
		{"a string where a bool belongs", map[string]any{
			"query": "needle", "case_sensitive": "yes",
		}},
		{"a word outside the closed set", map[string]any{
			"query": "needle", "mode": "fuzzy",
		}},
		{"an unpromised key inside a record", map[string]any{
			"query": "needle",
			"limit": map[string]any{"count": 10, "offset": 2},
		}},
		{"a record that is not a record", map[string]any{
			"query": "needle", "limit": "ten",
		}},
	}

	schema, err := capability.InputSchema()
	if err != nil {
		t.Fatalf("InputSchema: %v", err)
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bySchema := accepts(t, schema, tc.payload)
			byValidator := capability.ValidateInput(tc.payload) == nil
			if bySchema != byValidator {
				t.Errorf("schema says accept=%v, validator says accept=%v: they have drifted",
					bySchema, byValidator)
			}
		})
	}
}

// The unknown-key refusal is the one the schema can silently lose: drop
// additionalProperties and every payload still validates, so nothing else here
// would notice. It has to hold at depth too, because checkPayload recurses.
func TestUnknownKeysAreRefusedAtEveryDepth(t *testing.T) {
	schema, err := searchCapability().InputSchema()
	if err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	root, ok := schema["additionalProperties"].(bool)
	if !ok || root {
		t.Errorf("root additionalProperties = %v, want false", schema["additionalProperties"])
	}
	props, _ := schema["properties"].(map[string]any)
	nested, _ := props["limit"].(map[string]any)
	inner, ok := nested["additionalProperties"].(bool)
	if !ok || inner {
		t.Errorf("nested additionalProperties = %v, want false: checkPayload recurses and this does not",
			nested["additionalProperties"])
	}
}

// A set says which words may appear, never how many, so for a list it belongs
// on the element and not on the array.
func TestAClosedSetReachesTheNodeThatHoldsTheValue(t *testing.T) {
	capability := contract.Capability{
		ID: "code.search", Version: contract.Version{Major: 1}, Summary: "s",
		Inputs: []contract.Field{
			{Name: "mode", Type: contract.TypeString, Enum: []string{"regex", "literal"}},
			{Name: "kinds", Type: contract.TypeStringList, Enum: []string{"go", "md"}},
		},
		Outputs: []contract.Field{{Name: "hits", Type: contract.TypeInt, Required: true}},
		Effects: []contract.Effect{contract.EffectRead},
	}
	schema, err := capability.InputSchema()
	if err != nil {
		t.Fatalf("InputSchema: %v", err)
	}
	props := schema["properties"].(map[string]any)

	if _, on := props["mode"].(map[string]any)["enum"]; !on {
		t.Error("a string's set never reached its own node")
	}
	list := props["kinds"].(map[string]any)
	if _, onArray := list["enum"]; onArray {
		t.Error("the set landed on the array, which would constrain how many")
	}
	if _, onItems := list["items"].(map[string]any)["enum"]; !onItems {
		t.Error("the set never reached the element that holds the value")
	}
}

// A field the generator cannot describe is a refusal, not a silent omission:
// a schema missing a field advertises a capability that does not exist.
func TestAnUndescribableFieldIsRefused(t *testing.T) {
	capability := contract.Capability{
		ID: "code.search", Version: contract.Version{Major: 1}, Summary: "s",
		Inputs:  []contract.Field{{Name: "odd", Type: contract.FieldType(99)}},
		Outputs: []contract.Field{{Name: "hits", Type: contract.TypeInt, Required: true}},
		Effects: []contract.Effect{contract.EffectRead},
	}
	if _, err := capability.InputSchema(); contract.KindOf(err) != contract.FailureInvalidInput {
		t.Errorf("kind = %v, want invalid_input", contract.KindOf(err))
	}
}

// searchCapability exercises every field type the generator can describe,
// including a nested record, because the nesting is where the two walks can
// disagree without anything else noticing.
func searchCapability() contract.Capability {
	return contract.Capability{
		ID: "code.search", Version: contract.Version{Major: 1}, Summary: "Find literal text",
		Inputs: []contract.Field{
			{Name: "query", Type: contract.TypeString, Required: true, Summary: "what to find"},
			{Name: "case_sensitive", Type: contract.TypeBool},
			{Name: "paths", Type: contract.TypeStringList},
			{Name: "mode", Type: contract.TypeString, Enum: []string{"regex", "literal"}},
			{Name: "limit", Type: contract.TypeRecord, Fields: []contract.Field{
				{Name: "count", Type: contract.TypeInt, Required: true},
			}},
		},
		Outputs: []contract.Field{
			{Name: "hits", Type: contract.TypeInt, Required: true},
		},
		Effects: []contract.Effect{contract.EffectRead},
	}
}

// accepts is a JSON Schema reader that understands only what the generator
// emits. It exists to have an opinion the validator can contradict.
func accepts(t *testing.T, schema map[string]any, payload map[string]any) bool {
	t.Helper()
	return checkObject(schema, payload) == nil
}

func checkObject(schema map[string]any, payload map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	if extra, ok := schema["additionalProperties"].(bool); ok && !extra {
		for name := range payload {
			if _, declared := props[name]; !declared {
				return fmt.Errorf("unknown field %q", name)
			}
		}
	}
	if required, ok := schema["required"].([]string); ok {
		for _, name := range required {
			if _, present := payload[name]; !present {
				return fmt.Errorf("missing required field %q", name)
			}
		}
	}
	for name, value := range payload {
		node, declared := props[name].(map[string]any)
		if !declared {
			continue
		}
		if err := checkNode(node, value); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return nil
}

func checkNode(node map[string]any, value any) error {
	switch node["type"] {
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("want string, got %T", value)
		}
		return checkSet(node, text)
	case "integer":
		if _, ok := value.(int); !ok {
			return fmt.Errorf("want integer, got %T", value)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("want boolean, got %T", value)
		}
	case "array":
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("want array, got %T", value)
		}
		element, _ := node["items"].(map[string]any)
		for _, item := range items {
			if err := checkNode(element, item); err != nil {
				return err
			}
		}
	case "object":
		record, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("want object, got %T", value)
		}
		return checkObject(node, record)
	}
	return nil
}

func checkSet(node map[string]any, value string) error {
	set, ok := node["enum"].([]string)
	if !ok {
		return nil
	}
	for _, allowed := range set {
		if allowed == value {
			return nil
		}
	}
	return fmt.Errorf("%q is outside the declared set", value)
}
