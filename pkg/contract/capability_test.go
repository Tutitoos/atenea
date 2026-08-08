package contract_test

import (
	"strings"
	"testing"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// codeSearch mirrors the P0 capability: a required query, optional intent
// flags, and a list of records on the way out.
func codeSearch() contract.Capability {
	return contract.Capability{
		ID:        "code.search",
		Version:   contract.Version{Major: 1},
		Summary:   "Find literal text in a repository.",
		Semantics: "Flat text search.",
		Effects:   []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{Name: "query", Type: contract.TypeString, Required: true},
			{Name: "scope", Type: contract.TypeStringList},
			{Name: "regex", Type: contract.TypeBool},
			{Name: "context_lines", Type: contract.TypeInt},
		},
		Outputs: []contract.Field{
			{
				Name: "matches", Type: contract.TypeRecordList, Required: true,
				Fields: []contract.Field{
					{Name: "path", Type: contract.TypeString, Required: true},
					{Name: "line", Type: contract.TypeInt, Required: true},
					{Name: "snippet", Type: contract.TypeString},
				},
			},
		},
	}
}

// walk mirrors the shipped symbol.calls shape: a direction closed to three
// words, a list whose elements are closed to two, and one string left open.
func walk() contract.Capability {
	return contract.Capability{
		ID:        "symbol.calls",
		Version:   contract.Version{Major: 1},
		Summary:   "Walk the call graph from a symbol.",
		Semantics: "One hop at a time.",
		Effects:   []contract.Effect{contract.EffectRead},
		Inputs: []contract.Field{
			{
				Name: "direction", Type: contract.TypeString, Required: true,
				Enum: []string{"incoming", "outgoing", "both"},
			},
			{Name: "kinds", Type: contract.TypeStringList, Enum: []string{"function", "method"}},
		},
		Outputs: []contract.Field{
			{Name: "status", Type: contract.TypeString, Required: true},
		},
	}
}

func TestCapabilityValidateAcceptsTheP0Shape(t *testing.T) {
	if err := codeSearch().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestCapabilityValidateRejectsBadDefinitions(t *testing.T) {
	cases := map[string]func(*contract.Capability){
		"undotted id":       func(c *contract.Capability) { c.ID = "codesearch" },
		"uppercase id":      func(c *contract.Capability) { c.ID = "code.Search" },
		"missing version":   func(c *contract.Capability) { c.Version = contract.Version{} },
		"missing summary":   func(c *contract.Capability) { c.Summary = "  " },
		"duplicate input":   func(c *contract.Capability) { c.Inputs = append(c.Inputs, c.Inputs[0]) },
		"camelCase field":   func(c *contract.Capability) { c.Inputs[0].Name = "queryText " },
		"unknown effect":    func(c *contract.Capability) { c.Effects = []contract.Effect{99} },
		"record no fields":  func(c *contract.Capability) { c.Outputs[0].Fields = nil },
		"scalar has fields": func(c *contract.Capability) { c.Inputs[0].Fields = c.Outputs[0].Fields },
		"enum on an int":    func(c *contract.Capability) { c.Inputs[3].Enum = []string{"two"} },
		"enum on a record":  func(c *contract.Capability) { c.Outputs[0].Enum = []string{"a"} },
		"empty enum value":  func(c *contract.Capability) { c.Inputs[0].Enum = []string{""} },
		"repeated enum":     func(c *contract.Capability) { c.Inputs[0].Enum = []string{"a", "a"} },
		"reserved prefix":   func(c *contract.Capability) { c.ID = "raw.search" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			capability := codeSearch()
			mutate(&capability)
			if err := capability.Validate(); err == nil {
				t.Fatal("expected an error")
			} else if contract.KindOf(err) != contract.FailureInvalidInput {
				t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
			}
		})
	}
}

func TestValidateInputAcceptsAValidPayload(t *testing.T) {
	err := codeSearch().ValidateInput(map[string]any{
		"query":         "func main",
		"scope":         []string{"cmd", "internal"},
		"regex":         false,
		"context_lines": 4,
	})
	if err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
}

func TestValidateInputRequiresTheRequiredField(t *testing.T) {
	err := codeSearch().ValidateInput(map[string]any{"scope": []string{"cmd"}})
	if err == nil || !strings.Contains(err.Error(), `"query" is required`) {
		t.Fatalf("err = %v", err)
	}
}

// A caller passing a field the capability never promised is a caller leaning on
// one particular engine. Refusing it is what keeps implementations swappable.
func TestValidateInputRefusesUnknownFields(t *testing.T) {
	err := codeSearch().ValidateInput(map[string]any{
		"query":        "x",
		"ripgrep_pcre": true,
	})
	if err == nil || !strings.Contains(err.Error(), "unknown input field(s): ripgrep_pcre") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateInputChecksTypes(t *testing.T) {
	cases := map[string]map[string]any{
		"string given a number": {"query": 3},
		"bool given a string":   {"query": "x", "regex": "yes"},
		"int given a fraction":  {"query": "x", "context_lines": 2.5},
		"list given a scalar":   {"query": "x", "scope": "cmd"},
		"list of non-strings":   {"query": "x", "scope": []any{"cmd", 7}},
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if err := codeSearch().ValidateInput(payload); err == nil {
				t.Fatal("expected a type error")
			}
		})
	}
}

// A JSON decoder hands whole numbers back as float64. Adapters speak JSON, so
// refusing that would force every one of them to pre-convert.
func TestValidateInputAcceptsJSONWholeNumbers(t *testing.T) {
	err := codeSearch().ValidateInput(map[string]any{"query": "x", "context_lines": float64(4)})
	if err != nil {
		t.Fatalf("ValidateInput: %v", err)
	}
}

func TestValidateOutputWalksNestedRecords(t *testing.T) {
	capability := codeSearch()
	valid := map[string]any{
		"matches": []any{
			map[string]any{"path": "main.go", "line": 12, "snippet": "func main() {"},
			map[string]any{"path": "run.go", "line": 3},
		},
	}
	if err := capability.ValidateOutput(valid); err != nil {
		t.Fatalf("ValidateOutput: %v", err)
	}

	missingLine := map[string]any{
		"matches": []any{map[string]any{"path": "main.go"}},
	}
	if err := capability.ValidateOutput(missingLine); err == nil ||
		!strings.Contains(err.Error(), `"line" is required`) {
		t.Fatalf("err = %v", err)
	}

	wrongShape := map[string]any{"matches": []any{"main.go:12"}}
	if err := capability.ValidateOutput(wrongShape); err == nil {
		t.Fatal("expected a record type error")
	}
}

func TestCloneDoesNotShareState(t *testing.T) {
	original := codeSearch()
	clone := original.Clone()
	clone.Inputs[0].Name = "mutated"
	clone.Outputs[0].Fields[0].Name = "mutated"
	clone.Effects[0] = contract.EffectExternal

	if original.Inputs[0].Name != "query" {
		t.Error("clone shared the input slice")
	}
	if original.Outputs[0].Fields[0].Name != "path" {
		t.Error("clone shared the nested field slice")
	}
	if original.Effects[0] != contract.EffectRead {
		t.Error("clone shared the effect slice")
	}
}

func TestEveryValueOfAClosedSetIsAccepted(t *testing.T) {
	for _, value := range []string{"incoming", "outgoing", "both"} {
		if err := walk().ValidateInput(map[string]any{"direction": value}); err != nil {
			t.Errorf("%q: %v", value, err)
		}
	}
}

func TestAValueOutsideAClosedSetIsRefusedByName(t *testing.T) {
	err := walk().ValidateInput(map[string]any{"direction": "sideways"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("kind = %v, want invalid_input", contract.KindOf(err))
	}
	// The set has to travel with the refusal. A caller that guessed wrong
	// needs the list, not the verdict -- and the caller this field exists for
	// cannot be asked, so the message is the only place it learns. That makes
	// naming the set the load-bearing half, worth asserting rather than
	// assuming.
	for _, want := range []string{"incoming", "outgoing", "both"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not name %q: %v", want, err)
		}
	}
}

func TestAClosedSetConstrainsEachListElement(t *testing.T) {
	err := walk().ValidateInput(map[string]any{
		"direction": "both",
		"kinds":     []any{"function", "trait"},
	})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	// Which element failed, not just that one did: a list of ten with one bad
	// word is unreadable without the index.
	if !strings.Contains(err.Error(), "kinds[1]") {
		t.Errorf("refusal does not name the offending element: %v", err)
	}
}

func TestAStringWithNoDeclaredSetStaysOpen(t *testing.T) {
	// status carries no enum, and a provider reports in its own words. Adding
	// the field must not close what was never declared closed.
	if err := walk().ValidateOutput(map[string]any{"status": "reindexed 41 files"}); err != nil {
		t.Fatalf("open string refused: %v", err)
	}
}

func TestCloneDoesNotShareAClosedSet(t *testing.T) {
	original := walk()
	clone := original.Clone()
	clone.Inputs[0].Enum[0] = "mutated"
	if original.Inputs[0].Enum[0] != "incoming" {
		t.Error("clone shared the enum slice")
	}
}

func TestParseEffectAndFieldType(t *testing.T) {
	if e, err := contract.ParseEffect("external"); err != nil || e != contract.EffectExternal {
		t.Fatalf("ParseEffect = %v, %v", e, err)
	}
	if e, err := contract.ParseEffect("process"); err != nil || e != contract.EffectProcess {
		t.Fatalf("ParseEffect = %v, %v", e, err)
	}
	if got := contract.EffectProcess.String(); got != "process" {
		t.Fatalf("String = %q, want process", got)
	}
	if _, err := contract.ParseEffect("device"); err == nil {
		t.Fatal("unknown effect should fail")
	}
	if ft, err := contract.ParseFieldType("record_list"); err != nil || ft != contract.TypeRecordList {
		t.Fatalf("ParseFieldType = %v, %v", ft, err)
	}
	if _, err := contract.ParseFieldType("float"); err == nil {
		t.Fatal("unknown field type should fail")
	}
}
