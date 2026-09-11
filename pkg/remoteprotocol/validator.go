// Package remoteprotocol validates raw atenea.remote.v1 control envelopes.
package remoteprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"sync"

	protocolv1 "github.com/Tutitoos/atenea/protocol/atenea.remote.v1"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	// Subprotocol is the exact WebSocket subprotocol token for this contract.
	Subprotocol = "atenea.remote.v1"

	// MaxControlMessageSize is the maximum number of bytes accepted for one
	// control message. It is the same 16 MiB cap represented by the protocol's
	// oversize diagnostic schema.
	MaxControlMessageSize = 16 << 20

	// MaxMessageBytes is retained as a descriptive alias for callers that name
	// the limit in terms of the wire message rather than its control role.
	MaxMessageBytes = MaxControlMessageSize
	// MaxControlMessageBytes is the byte-oriented spelling of the same limit.
	MaxControlMessageBytes = MaxControlMessageSize

	// MaxJSONNestingDepth is the maximum number of nested JSON containers,
	// including the root container. It is deliberately below the parser and
	// schema library limits so hostile nesting cannot grow the scan stack
	// without bound.
	MaxJSONNestingDepth = 64

	// MaxJSONNumberTokenLength bounds the amount of numeric text retained by
	// json.Decoder before jsonschema sees it.
	MaxJSONNumberTokenLength = 128
	// MaxJSONNumberSignificandDigits bounds the decimal digits in a number's
	// mantissa. It is wider than every numeric range in the protocol schema.
	MaxJSONNumberSignificandDigits = 64
	// MaxJSONNumberExponentMagnitude keeps big.Rat construction comfortably
	// below its large-exponent failure threshold while covering protocol
	// values expressed in ordinary scientific notation.
	MaxJSONNumberExponentMagnitude = 308
)

// ErrorCategory is the stable, payload-independent class of a validation
// failure.
type ErrorCategory string

// CategoryEmpty through CategorySchemaInvalid identify stable classes of
// validation failures independent of the rejected payload. The descriptive
// aliases name the corresponding primary categories.
const (
	CategoryEmpty       ErrorCategory = "empty"
	CategoryOversize    ErrorCategory = "oversize"
	CategoryMalformed   ErrorCategory = "malformed_json"
	CategoryDuplicate   ErrorCategory = "duplicate_key"
	CategoryTrailing    ErrorCategory = "trailing_json"
	CategoryNonObject   ErrorCategory = "non_object"
	CategoryDepth       ErrorCategory = "nesting_depth"
	CategoryNumeric     ErrorCategory = "numeric_resource"
	CategorySchema      ErrorCategory = "schema"
	CategoryUnavailable ErrorCategory = "validator_unavailable"

	CategoryInvalidJSON     = CategoryMalformed
	CategoryDuplicateKey    = CategoryDuplicate
	CategoryTrailingJSON    = CategoryTrailing
	CategoryNotObject       = CategoryNonObject
	CategoryNestingDepth    = CategoryDepth
	CategoryNumber          = CategoryNumeric
	CategoryNumericResource = CategoryNumeric
	CategorySchemaInvalid   = CategorySchema
)

// ValidationError is returned for every rejected raw envelope. It contains
// only a stable category; raw bytes, field values, and schema diagnostics are
// deliberately not retained or rendered.
type ValidationError struct {
	Category ErrorCategory
}

func (e *ValidationError) Error() string {
	if e == nil {
		return "remoteprotocol: validation error"
	}
	return "remoteprotocol: " + string(e.Category)
}

// ErrEmpty through ErrSchemaInvalid are immutable errors.Is sentinels for
// validation categories. The descriptive aliases refer to those same
// sentinels.
var (
	ErrEmpty       error = validationSentinel(CategoryEmpty)
	ErrOversize    error = validationSentinel(CategoryOversize)
	ErrMalformed   error = validationSentinel(CategoryMalformed)
	ErrDuplicate   error = validationSentinel(CategoryDuplicate)
	ErrTrailing    error = validationSentinel(CategoryTrailing)
	ErrNonObject   error = validationSentinel(CategoryNonObject)
	ErrDepth       error = validationSentinel(CategoryDepth)
	ErrNumeric     error = validationSentinel(CategoryNumeric)
	ErrSchema      error = validationSentinel(CategorySchema)
	ErrUnavailable error = validationSentinel(CategoryUnavailable)

	ErrInvalidJSON   = ErrMalformed
	ErrDuplicateKey  = ErrDuplicate
	ErrTrailingJSON  = ErrTrailing
	ErrNotObject     = ErrNonObject
	ErrNestingDepth  = ErrDepth
	ErrNumber        = ErrNumeric
	ErrSchemaInvalid = ErrSchema
)

// validationSentinel is an immutable errors.Is target. ValidationError has an
// exported category for API compatibility, so exported sentinel pointers
// would be shared mutable state even though Validate returns fresh errors.
type validationSentinel ErrorCategory

func (e validationSentinel) Error() string {
	return "remoteprotocol: " + string(e)
}

func (e validationSentinel) Is(target error) bool {
	other, ok := target.(validationSentinel)
	return ok && e == other
}

// Is reports whether target represents the same validation category as e.
func (e *ValidationError) Is(target error) bool {
	if e == nil {
		return false
	}
	switch other := target.(type) {
	case *ValidationError:
		return other != nil && e.Category == other.Category
	case validationSentinel:
		return e.Category == ErrorCategory(other)
	default:
		return false
	}
}

// CategoryOf returns the stable category of a validation error. Unknown
// errors return the empty category.
func CategoryOf(err error) ErrorCategory {
	var validationErr *ValidationError
	if errors.As(err, &validationErr) {
		if validationErr == nil {
			return ""
		}
		return validationErr.Category
	}
	var sentinel validationSentinel
	if errors.As(err, &sentinel) {
		return ErrorCategory(sentinel)
	}
	return ""
}

// Validator is an immutable, concurrency-safe compiled envelope validator.
// Its schema is compiled once from the embedded canonical assets; Validate
// allocates only per-call parser state.
type Validator struct {
	schema *jsonschema.Schema
}

var compiledEnvelope struct {
	once   sync.Once
	schema *jsonschema.Schema
	err    error
}

// NewValidator constructs a validator from the embedded Draft 2020-12 schema
// set. A compilation error indicates a broken bundled contract, rather than a
// property of a caller's message.
func NewValidator() (*Validator, error) {
	compiledEnvelope.once.Do(func() {
		compiledEnvelope.schema, compiledEnvelope.err = compileEnvelopeSchema()
	})
	if compiledEnvelope.err != nil {
		return nil, compiledEnvelope.err
	}
	return &Validator{schema: compiledEnvelope.schema}, nil
}

// MustNewValidator constructs a validator and panics only when the bundled
// canonical schemas cannot compile.
func MustNewValidator() *Validator {
	validator, err := NewValidator()
	if err != nil {
		panic(err)
	}
	return validator
}

// New is a concise constructor for applications whose canonical assets are
// part of the same binary and therefore cannot be replaced at runtime.
func New() *Validator { return MustNewValidator() }

// Validate checks one complete raw JSON envelope. It does not mutate raw and
// never includes raw content in an error.
func (v *Validator) Validate(raw []byte) error {
	if v == nil || v.schema == nil {
		return newValidationError(CategoryUnavailable)
	}
	if len(raw) == 0 {
		return newValidationError(CategoryEmpty)
	}
	if len(raw) > MaxControlMessageSize {
		return newValidationError(CategoryOversize)
	}
	if len(bytes.Trim(raw, " \t\r\n")) == 0 {
		return newValidationError(CategoryEmpty)
	}

	if category := preflightJSON(raw); category != "" {
		return newValidationError(category)
	}

	rootObject, category := scanJSON(raw)
	if category != "" {
		return newValidationError(category)
	}
	if !rootObject {
		return newValidationError(CategoryNonObject)
	}

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return newValidationError(CategoryMalformed)
	}
	if err := v.schema.Validate(instance); err != nil {
		return newValidationError(CategorySchema)
	}
	return nil
}

func newValidationError(category ErrorCategory) error {
	return &ValidationError{Category: category}
}

const envelopeSchemaURL = "https://schemas.atenea.invalid/atenea.remote.v1/envelope.schema.json"

type embeddedSchemaLoader struct{}

func (embeddedSchemaLoader) Load(location string) (any, error) {
	u, err := url.Parse(location)
	if err != nil {
		return nil, err
	}
	name := path.Base(u.Path)
	switch name {
	case "binary-frame.schema.json", "definitions.schema.json", "envelope.schema.json",
		"event-ack.schema.json",
		"error.schema.json", "event.schema.json", "heartbeat.schema.json",
		"negotiation.schema.json", "request.schema.json", "result.schema.json":
	default:
		return nil, fmt.Errorf("unknown embedded schema resource")
	}
	raw, err := protocolv1.FS.ReadFile(name)
	if err != nil {
		return nil, err
	}
	return jsonschema.UnmarshalJSON(bytes.NewReader(raw))
}

func compileEnvelopeSchema() (*jsonschema.Schema, error) {
	loader := embeddedSchemaLoader{}
	root, err := loader.Load(envelopeSchemaURL)
	if err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(loader)
	if err := compiler.AddResource(envelopeSchemaURL, root); err != nil {
		return nil, err
	}
	return compiler.Compile(envelopeSchemaURL)
}

// scanJSON performs a syntax-only pass that preserves JSON numbers and, unlike
// encoding/json's ordinary unmarshaler, rejects duplicate object names at any
// nesting depth. It also proves that exactly one complete value was supplied.
func scanJSON(raw []byte) (rootObject bool, category ErrorCategory) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return false, CategoryMalformed
	}
	rootObject = isObjectDelimiter(first)
	if category := scanValue(decoder, first, 0); category != "" {
		return false, category
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return false, CategoryTrailing
		}
		return false, CategoryMalformed
	}
	return rootObject, ""
}

// preflightJSON performs the bounded part of number lexing directly on raw
// bytes. In particular, it does not let encoding/json first allocate a
// json.Number containing an attacker-sized token. Strings and escaped bytes
// are skipped as opaque JSON string contents; structural validation remains
// the responsibility of scanJSON and the schema validator.
func preflightJSON(raw []byte) ErrorCategory {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '"' {
			i++
			for i < len(raw) {
				switch raw[i] {
				case '\\':
					i += 2
				case '"':
					goto stringDone
				default:
					i++
				}
			}
			return ""
		}

		if (raw[i] == '-' || isASCIIDigit(raw[i])) && startsJSONNumber(raw, i) {
			next, category := preflightNumber(raw, i)
			if category != "" {
				return category
			}
			i = next - 1
		}

	stringDone:
	}
	return ""
}

func startsJSONNumber(raw []byte, index int) bool {
	return index < len(raw) && (raw[index] == '-' || isASCIIDigit(raw[index]))
}

func preflightNumber(raw []byte, start int) (next int, category ErrorCategory) {
	i := start
	length := 0
	significandDigits := 0

	consume := func() bool {
		length++
		return length <= MaxJSONNumberTokenLength
	}
	if raw[i] == '-' {
		if !consume() {
			return i + 1, CategoryNumeric
		}
		i++
	}
	if i == len(raw) || !isASCIIDigit(raw[i]) {
		return i, ""
	}

	if raw[i] == '0' {
		if !consume() {
			return i + 1, CategoryNumeric
		}
		i++
		significandDigits = 1
	} else {
		for i < len(raw) && isASCIIDigit(raw[i]) {
			if !consume() {
				return i + 1, CategoryNumeric
			}
			i++
			significandDigits++
			if significandDigits > MaxJSONNumberSignificandDigits {
				return i, CategoryNumeric
			}
		}
	}

	if i < len(raw) && raw[i] == '.' {
		if !consume() {
			return i + 1, CategoryNumeric
		}
		i++
		for i < len(raw) && isASCIIDigit(raw[i]) {
			if !consume() {
				return i + 1, CategoryNumeric
			}
			i++
			significandDigits++
			if significandDigits > MaxJSONNumberSignificandDigits {
				return i, CategoryNumeric
			}
		}
	}

	if i < len(raw) && (raw[i] == 'e' || raw[i] == 'E') {
		if !consume() {
			return i + 1, CategoryNumeric
		}
		i++
		if i < len(raw) && (raw[i] == '+' || raw[i] == '-') {
			if !consume() {
				return i + 1, CategoryNumeric
			}
			i++
		}
		exponent := 0
		for i < len(raw) && isASCIIDigit(raw[i]) {
			if !consume() {
				return i + 1, CategoryNumeric
			}
			digit := int(raw[i] - '0')
			i++
			if exponent > MaxJSONNumberExponentMagnitude/10 ||
				(exponent == MaxJSONNumberExponentMagnitude/10 && digit > MaxJSONNumberExponentMagnitude%10) {
				return i, CategoryNumeric
			}
			exponent = exponent*10 + digit
		}
	}

	return i, ""
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func scanValue(decoder *json.Decoder, token json.Token, depth int) ErrorCategory {
	delim, ok := token.(json.Delim)
	if !ok {
		if number, ok := token.(json.Number); ok && !validJSONNumber(number) {
			return CategoryNumeric
		}
		return ""
	}
	switch delim {
	case '{':
		if depth >= MaxJSONNestingDepth {
			return CategoryDepth
		}
		seen := make(map[string]struct{})
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return CategoryMalformed
			}
			keyString, ok := key.(string)
			if !ok {
				return CategoryMalformed
			}
			if _, exists := seen[keyString]; exists {
				return CategoryDuplicate
			}
			seen[keyString] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return CategoryMalformed
			}
			if category := scanValue(decoder, value, depth+1); category != "" {
				return category
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return CategoryMalformed
		}
	case '[':
		if depth >= MaxJSONNestingDepth {
			return CategoryDepth
		}
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return CategoryMalformed
			}
			if category := scanValue(decoder, value, depth+1); category != "" {
				return category
			}
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return CategoryMalformed
		}
	case '}', ']':
		return CategoryMalformed
	}
	return ""
}

// validJSONNumber applies a resource policy to a syntactically valid JSON
// number without converting it to float64 or any other imprecise type. The
// decoder has already checked JSON syntax; spelling it out here keeps the
// bounds local to the first scan and makes the policy independent of decoder
// implementation details.
func validJSONNumber(number json.Number) bool {
	token := number.String()
	if len(token) == 0 || len(token) > MaxJSONNumberTokenLength {
		return false
	}

	i := 0
	if token[i] == '-' {
		i++
	}
	if i == len(token) {
		return false
	}

	significandDigits := 0
	if token[i] == '0' {
		i++
		significandDigits++
		if i < len(token) && token[i] >= '0' && token[i] <= '9' {
			return false
		}
	} else {
		if token[i] < '1' || token[i] > '9' {
			return false
		}
		for i < len(token) && token[i] >= '0' && token[i] <= '9' {
			i++
			significandDigits++
		}
	}

	if i < len(token) && token[i] == '.' {
		i++
		fractionStart := i
		for i < len(token) && token[i] >= '0' && token[i] <= '9' {
			i++
			significandDigits++
		}
		if i == fractionStart {
			return false
		}
	}
	if significandDigits > MaxJSONNumberSignificandDigits {
		return false
	}

	if i < len(token) && (token[i] == 'e' || token[i] == 'E') {
		i++
		if i < len(token) && (token[i] == '+' || token[i] == '-') {
			i++
		}
		exponentStart := i
		exponent := 0
		for i < len(token) && token[i] >= '0' && token[i] <= '9' {
			digit := int(token[i] - '0')
			if exponent > MaxJSONNumberExponentMagnitude/10 ||
				(exponent == MaxJSONNumberExponentMagnitude/10 && digit > MaxJSONNumberExponentMagnitude%10) {
				return false
			}
			exponent = exponent*10 + digit
			i++
		}
		if i == exponentStart {
			return false
		}
	}

	return i == len(token)
}

func isObjectDelimiter(token json.Token) bool {
	delim, ok := token.(json.Delim)
	return ok && delim == '{'
}
