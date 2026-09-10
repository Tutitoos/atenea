package remoteprotocol

import (
	"errors"
	"strings"
	"testing"
)

const validHeartbeat = `{"protocol":"atenea.remote.v1","version":"1.0.0","message_type":"heartbeat","session_id":"s","device_id":"d","sequence":0,"sent_at":0,"payload":{"kind":"ping","interval_ms":1000,"liveness_deadline_ms":1000,"monotonic_tick":0}}`

const validNegotiationOffer = `{"protocol":"atenea.remote.v1","version":"1.0.0","message_type":"negotiation","session_id":"s","device_id":"d","sequence":0,"sent_at":0,"payload":{"phase":"offer","role":"agent","agent_version":"1","platform":"windows","architecture":"x86_64","supported_versions":["1.0.0"],"modes":["attended"],"capabilities":[]}}`

func TestValidatorAcceptsRepresentativeEnvelope(t *testing.T) {
	validator, err := NewValidator()
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"heartbeat":   validHeartbeat,
		"negotiation": validNegotiationOffer,
	} {
		t.Run(name, func(t *testing.T) {
			if err := validator.Validate([]byte(raw)); err != nil {
				t.Fatalf("valid envelope rejected: %v", err)
			}
		})
	}
}

func TestValidatorRejectsParserBoundaries(t *testing.T) {
	validator := MustNewValidator()
	tests := []struct {
		name string
		raw  string
		want ErrorCategory
	}{
		{name: "empty", raw: "", want: CategoryEmpty},
		{name: "whitespace empty", raw: " \n\t", want: CategoryEmpty},
		{name: "vertical tab is malformed", raw: "\v", want: CategoryMalformed},
		{name: "duplicate root key", raw: `{"protocol":"atenea.remote.v1","protocol":"atenea.remote.v1"}`, want: CategoryDuplicate},
		{name: "duplicate nested key", raw: `{"payload":{"x":1,"x":2}}`, want: CategoryDuplicate},
		{name: "trailing json", raw: validHeartbeat + ` {}`, want: CategoryTrailing},
		{name: "non-object", raw: `[]`, want: CategoryNonObject},
		{name: "malformed", raw: `{"protocol":`, want: CategoryMalformed},
		{name: "leading zero", raw: `{"sequence":01}`, want: CategoryMalformed},
		{name: "leading plus sign", raw: `{"sequence":+1}`, want: CategoryMalformed},
		{name: "incomplete exponent", raw: `{"sequence":1E}`, want: CategoryMalformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.Validate([]byte(test.raw))
			if got := CategoryOf(err); got != test.want {
				t.Fatalf("category = %q, want %q (err=%v)", got, test.want, err)
			}
		})
	}
}

func TestValidatorTreatsVerticalTabAsMalformedJSON(t *testing.T) {
	if got := CategoryOf(MustNewValidator().Validate([]byte{0x0b})); got != CategoryMalformed {
		t.Fatalf("vertical-tab category = %q, want %q", got, CategoryMalformed)
	}
}

func TestValidatorRejectsDepthBeforeSchemaValidation(t *testing.T) {
	validator := MustNewValidator()
	within := nestedObjectValue(MaxJSONNestingDepth)
	if _, category := scanJSON([]byte(within)); category != "" {
		t.Fatalf("just-within depth scan category = %q", category)
	}

	over := nestedObjectValue(MaxJSONNestingDepth + 1)
	assertNoPanic(t, func() error { return validator.Validate([]byte(over)) }, CategoryDepth)
}

func TestValidatorRejectsUnsafeNumbersBeforeSchemaValidation(t *testing.T) {
	validator := MustNewValidator()
	for _, raw := range []string{
		strings.Replace(validHeartbeat, `"sequence":0`, `"sequence":1e309`, 1),
		strings.Replace(validHeartbeat, `"sequence":0`, `"sequence":1e-309`, 1),
		strings.Replace(validHeartbeat, `"sequence":0`, `"sequence":1e1000000000`, 1),
	} {
		assertNoPanic(t, func() error { return validator.Validate([]byte(raw)) }, CategoryNumeric)
	}
}

func TestValidatorAcceptsNumericResourceBoundaries(t *testing.T) {
	validator := MustNewValidator()
	tests := []struct {
		name   string
		number string
	}{
		{name: "token length 128", number: "0e+" + strings.Repeat("0", 125)},
		{name: "significand 64", number: "0." + strings.Repeat("0", 63)},
		{name: "positive exponent 308", number: "0e+308"},
		{name: "negative exponent 308", number: "0e-308"},
		{name: "negative sign", number: "-0"},
		{name: "negative sign and uppercase exponent", number: "-0E-000308"},
		{name: "uppercase exponent with leading zeros", number: "0E+000308"},
		{name: "uppercase negative exponent with leading zeros", number: "0E-000308"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Replace(validHeartbeat, `"sequence":0`, `"sequence":`+test.number, 1)
			if err := validator.Validate([]byte(raw)); err != nil {
				t.Fatalf("boundary number rejected: %v", err)
			}
		})
	}
}

func TestValidatorRejectsNumericResourceLimits(t *testing.T) {
	validator := MustNewValidator()
	tests := []struct {
		name   string
		number string
	}{
		{name: "token length 129", number: "0e+" + strings.Repeat("0", 126)},
		{name: "significand 65", number: "0." + strings.Repeat("0", 64)},
		{name: "positive exponent 309", number: "0e+309"},
		{name: "negative exponent 309", number: "0e-309"},
		{name: "huge negative exponent", number: "0e-" + strings.Repeat("9", 1024)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := strings.Replace(validHeartbeat, `"sequence":0`, `"sequence":`+test.number, 1)
			if got := CategoryOf(validator.Validate([]byte(raw))); got != CategoryNumeric {
				t.Fatalf("category = %q, want %q", got, CategoryNumeric)
			}
		})
	}
}

func TestNumericPreflightSkipsStringsAndEscapes(t *testing.T) {
	large := strings.Repeat("9", MaxJSONNumberTokenLength*8)
	raw := []byte(`{"text":"` + large + `e-999\"","escaped":"1e+` + large + `","value":0}`)
	if got := preflightJSON(raw); got != "" {
		t.Fatalf("string content triggered numeric preflight: %q", got)
	}
}

func TestNumericPreflightDoesNotAllocateAttackerSizedNumber(t *testing.T) {
	raw := []byte(`{"value":` + strings.Repeat("9", MaxJSONNumberTokenLength*1024) + `}`)
	if got := preflightJSON(raw); got != CategoryNumeric {
		t.Fatalf("preflight category = %q, want %q", got, CategoryNumeric)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		if got := preflightJSON(raw); got != CategoryNumeric {
			t.Fatalf("preflight category = %q, want %q", got, CategoryNumeric)
		}
	}); allocations != 0 {
		t.Fatalf("preflight allocations = %v, want zero", allocations)
	}
}

func TestValidatorRejectsConcatenatedAttackerSizedNumbersBeforeDecode(t *testing.T) {
	validator := MustNewValidator()
	largeNumber := strings.Repeat("9", MaxJSONNumberTokenLength*1024)
	tests := []struct {
		name string
		root string
	}{
		{name: "object", root: `{}`},
		{name: "array", root: `[]`},
		{name: "string", root: `"root"`},
		{name: "true", root: `true`},
		{name: "false", root: `false`},
		{name: "null", root: `null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.root + largeNumber)
			if got := CategoryOf(validator.Validate(raw)); got != CategoryNumeric {
				t.Fatalf("Validate category = %q, want %q", got, CategoryNumeric)
			}
			if got := preflightJSON(raw); got != CategoryNumeric {
				t.Fatalf("preflight category = %q, want %q", got, CategoryNumeric)
			}
			if allocations := testing.AllocsPerRun(100, func() {
				if got := preflightJSON(raw); got != CategoryNumeric {
					t.Fatalf("preflight category = %q, want %q", got, CategoryNumeric)
				}
			}); allocations != 0 {
				t.Fatalf("preflight allocations = %v, want zero", allocations)
			}
		})
	}
}

func TestValidatorRejectsNumericRootAttackerSizedNumbersBeforeDecode(t *testing.T) {
	validator := MustNewValidator()
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "adjacent digit", raw: []byte(`0` + strings.Repeat(`9`, 131072))},
		{name: "minus then digit", raw: []byte(`1-` + strings.Repeat(`9`, 131072))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := preflightJSON(test.raw); got != CategoryNumeric {
				t.Fatalf("preflight category = %q, want %q", got, CategoryNumeric)
			}
			if got := CategoryOf(validator.Validate(test.raw)); got != CategoryNumeric {
				t.Fatalf("Validate category = %q, want %q", got, CategoryNumeric)
			}
			if allocations := testing.AllocsPerRun(100, func() {
				if got := preflightJSON(test.raw); got != CategoryNumeric {
					t.Fatalf("preflight category = %q, want %q", got, CategoryNumeric)
				}
			}); allocations != 0 {
				t.Fatalf("preflight allocations = %v, want zero", allocations)
			}
		})
	}
}

func TestCategoryOfTypedNilValidationError(t *testing.T) {
	var validationErr *ValidationError
	if got := CategoryOf(validationErr); got != "" {
		t.Fatalf("typed-nil category = %q, want empty fallback", got)
	}
	var err error = validationErr
	if got := CategoryOf(err); got != "" {
		t.Fatalf("typed-nil interface category = %q, want empty fallback", got)
	}
}

func TestValidationErrorsAreFreshAndMutationIsolated(t *testing.T) {
	var unavailable *Validator
	first := unavailable.Validate([]byte(validHeartbeat))
	second := unavailable.Validate([]byte(validHeartbeat))
	if first == second {
		t.Fatal("unavailable validation returned a shared error")
	}
	first.(*ValidationError).Category = CategorySchema
	if got := CategoryOf(second); got != CategoryUnavailable {
		t.Fatalf("second category = %q after first mutation, want %q", got, CategoryUnavailable)
	}
	if got := CategoryOf(ErrUnavailable); got != CategoryUnavailable {
		t.Fatalf("unavailable sentinel category = %q, want %q", got, CategoryUnavailable)
	}
}

func TestValidationErrorsAreFreshAcrossCategories(t *testing.T) {
	validator := MustNewValidator()
	largeNumber := `{"value":` + strings.Repeat("9", MaxJSONNumberTokenLength+1) + `}`
	depth := nestedObjectValue(MaxJSONNestingDepth + 1)
	tests := []struct {
		name string
		raw  []byte
		want ErrorCategory
	}{
		{name: "empty", raw: nil, want: CategoryEmpty},
		{name: "oversize", raw: []byte(strings.Repeat("x", MaxControlMessageSize+1)), want: CategoryOversize},
		{name: "malformed", raw: []byte(`{"protocol":`), want: CategoryMalformed},
		{name: "duplicate", raw: []byte(`{"x":1,"x":2}`), want: CategoryDuplicate},
		{name: "trailing", raw: []byte(validHeartbeat + ` {}`), want: CategoryTrailing},
		{name: "non-object", raw: []byte(`[]`), want: CategoryNonObject},
		{name: "depth", raw: []byte(depth), want: CategoryDepth},
		{name: "numeric", raw: []byte(largeNumber), want: CategoryNumeric},
		{name: "schema", raw: []byte(`{}`), want: CategorySchema},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first := validator.Validate(test.raw)
			second := validator.Validate(test.raw)
			if first == second {
				t.Fatal("validation returned a shared error")
			}
			firstValidation, ok := first.(*ValidationError)
			if !ok {
				t.Fatalf("error type = %T, want *ValidationError", first)
			}
			firstValidation.Category = CategoryUnavailable
			if got := CategoryOf(second); got != test.want {
				t.Fatalf("second category = %q after first mutation, want %q", got, test.want)
			}
		})
	}
}

func TestValidatorRejectsEscapedAndNestedDuplicateKeys(t *testing.T) {
	validator := MustNewValidator()
	tests := []string{
		`{"protocol":"atenea.remote.v1","pro\u0074ocol":"atenea.remote.v1"}`,
		`{"payload":[{"x":1,"x":2}]}`,
	}
	for _, raw := range tests {
		if err := validator.Validate([]byte(raw)); !errors.Is(err, ErrDuplicate) {
			t.Fatalf("error = %v, want duplicate-key category", err)
		}
	}
}

func TestValidatorRejectsMalformedTrailingContent(t *testing.T) {
	validator := MustNewValidator()
	err := validator.Validate([]byte(validHeartbeat + ` trailing`))
	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("error = %v, want malformed trailing content", err)
	}
}

func TestValidatorRejectsOversizeWithoutDecoding(t *testing.T) {
	validator := MustNewValidator()
	raw := strings.Repeat("x", MaxControlMessageSize+1)
	err := validator.Validate([]byte(raw))
	if !errors.Is(err, ErrOversize) {
		t.Fatalf("error = %v, want oversize", err)
	}
}

func TestValidatorRejectsSchemaBoundariesWithoutPayloadLeakage(t *testing.T) {
	validator := MustNewValidator()
	tests := []struct {
		name string
		raw  string
	}{
		{name: "unknown field", raw: strings.TrimSuffix(validHeartbeat, `}`) + `,"unexpected":"secret-value"}`},
		{name: "invalid heartbeat interval", raw: strings.Replace(validHeartbeat, `"interval_ms":1000`, `"interval_ms":999`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validator.Validate([]byte(test.raw))
			if !errors.Is(err, ErrSchema) {
				t.Fatalf("error = %v, want schema", err)
			}
			if strings.Contains(err.Error(), "secret-value") || strings.Contains(err.Error(), "unexpected") {
				t.Fatalf("error leaked payload: %v", err)
			}
		})
	}
}

func TestValidatorIsSafeForConcurrentUse(t *testing.T) {
	validator := MustNewValidator()
	t.Parallel()
	for i := 0; i < 32; i++ {
		t.Run("parallel", func(t *testing.T) {
			t.Parallel()
			if err := validator.Validate([]byte(validHeartbeat)); err != nil {
				t.Errorf("valid heartbeat rejected: %v", err)
			}
		})
	}
}

func nestedObjectValue(depth int) string {
	if depth < 1 {
		return `0`
	}
	return `{"x":` + strings.Repeat(`[`, depth-1) + `0` + strings.Repeat(`]`, depth-1) + `}`
}

func assertNoPanic(t *testing.T, validate func() error, want ErrorCategory) {
	t.Helper()
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("validation panicked: %v", recovered)
		}
	}()
	if err := validate(); CategoryOf(err) != want {
		t.Fatalf("category = %q, want %q (err=%v)", CategoryOf(err), want, err)
	}
}
