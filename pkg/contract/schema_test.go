package contract_test

import (
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

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
