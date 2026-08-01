package contract

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Effect is an observable consequence of running a capability.
//
// There are three groups on purpose. Writing breaks something of your own, at
// home, and can be undone. Reaching outside escapes the machine and no undo
// takes it back. Putting both in the same bag would give the dangerous one the
// permissions of the harmless one.
type Effect uint8

const (
	// EffectRead touches the filesystem read-only.
	EffectRead Effect = iota
	// EffectWrite creates, edits or deletes.
	EffectWrite
	// EffectExternal leaves the machine: network, external services.
	EffectExternal
)

var (
	effectNames = map[Effect]string{
		EffectRead:     "read",
		EffectWrite:    "write",
		EffectExternal: "external",
	}
	effectByName = map[string]Effect{
		"read":     EffectRead,
		"write":    EffectWrite,
		"external": EffectExternal,
	}
)

func (e Effect) String() string {
	if name, ok := effectNames[e]; ok {
		return name
	}
	return fmt.Sprintf("effect(%d)", uint8(e))
}

// ParseEffect reads an effect name.
func ParseEffect(s string) (Effect, error) {
	if e, ok := effectByName[s]; ok {
		return e, nil
	}
	return 0, fmt.Errorf("unknown effect %q: want read, write or external", s)
}

// FieldType is the type of a single input or output field. The set is
// deliberately tiny: a capability contract has to be checkable, not expressive.
type FieldType uint8

// The field types a capability may declare.
const (
	TypeString FieldType = iota
	TypeStringList
	TypeInt
	TypeBool
	// TypeRecord is a nested object described by Field.Fields.
	TypeRecord
	// TypeRecordList is a list of TypeRecord values.
	TypeRecordList
)

var (
	typeNames = map[FieldType]string{
		TypeString:     "string",
		TypeStringList: "string_list",
		TypeInt:        "int",
		TypeBool:       "bool",
		TypeRecord:     "record",
		TypeRecordList: "record_list",
	}
	typeByName = map[string]FieldType{
		"string":      TypeString,
		"string_list": TypeStringList,
		"int":         TypeInt,
		"bool":        TypeBool,
		"record":      TypeRecord,
		"record_list": TypeRecordList,
	}
)

func (t FieldType) String() string {
	if name, ok := typeNames[t]; ok {
		return name
	}
	return fmt.Sprintf("type(%d)", uint8(t))
}

// ParseFieldType reads a field type name.
func ParseFieldType(s string) (FieldType, error) {
	if t, ok := typeByName[s]; ok {
		return t, nil
	}
	return 0, fmt.Errorf("unknown field type %q", s)
}

// Field is one entry of a capability's input or output shape.
type Field struct {
	Name     string
	Type     FieldType
	Required bool
	Summary  string
	// Fields describes the members of a record or record_list. It must be empty
	// for every other type.
	Fields []Field
}

// Capability is an action Atenea can ask for without knowing who will run it.
//
// It is the stable half of the design: the "what". Everything that varies by
// tool -- languages, indexes, cost, health -- belongs to Implementation, the
// "who". A capability that grew a restriction would stop being swappable, which
// is the whole point of having one.
type Capability struct {
	ID      string
	Version Version
	Summary string
	// Semantics is the guarantee callers may rely on, in prose. It is the part
	// that keeps two implementations honestly interchangeable.
	Semantics string
	Inputs    []Field
	Outputs   []Field
	Effects   []Effect
}

var (
	capabilityID = regexp.MustCompile(`^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$`)
	fieldName    = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
)

// Validate checks the capability definition itself.
func (c Capability) Validate() error {
	if !capabilityID.MatchString(c.ID) {
		return Fail(FailureInvalidInput,
			"capability id %q must be dotted lowercase, e.g. code.search", c.ID)
	}
	if c.Version == (Version{}) {
		return Fail(FailureInvalidInput, "capability %s: version is required", c.ID)
	}
	if strings.TrimSpace(c.Summary) == "" {
		return Fail(FailureInvalidInput, "capability %s: summary is required", c.ID)
	}
	if err := validateFields("input", c.ID, c.Inputs); err != nil {
		return err
	}
	if err := validateFields("output", c.ID, c.Outputs); err != nil {
		return err
	}
	for _, e := range c.Effects {
		if _, ok := effectNames[e]; !ok {
			return Fail(FailureInvalidInput, "capability %s: unknown effect %d", c.ID, e)
		}
	}
	return nil
}

func validateFields(kind, capID string, fields []Field) error {
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if !fieldName.MatchString(f.Name) {
			return Fail(FailureInvalidInput,
				"capability %s: %s name %q must be lowercase snake_case", capID, kind, f.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return Fail(FailureInvalidInput,
				"capability %s: duplicate %s %q", capID, kind, f.Name)
		}
		seen[f.Name] = struct{}{}
		if _, ok := typeNames[f.Type]; !ok {
			return Fail(FailureInvalidInput,
				"capability %s: %s %q has unknown type %d", capID, kind, f.Name, f.Type)
		}
		nested := f.Type == TypeRecord || f.Type == TypeRecordList
		switch {
		case nested && len(f.Fields) == 0:
			return Fail(FailureInvalidInput,
				"capability %s: %s %q is a %s and needs nested fields", capID, kind, f.Name, f.Type)
		case !nested && len(f.Fields) > 0:
			return Fail(FailureInvalidInput,
				"capability %s: %s %q is a %s and cannot have nested fields", capID, kind, f.Name, f.Type)
		}
		if nested {
			if err := validateFields(kind, capID, f.Fields); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateInput checks a payload against the declared inputs. Unknown keys are
// refused: a caller passing a field the capability never promised is a caller
// relying on one particular implementation.
func (c Capability) ValidateInput(payload map[string]any) error {
	return checkPayload(c.ID, "input", c.Inputs, payload)
}

// ValidateOutput checks a payload against the declared outputs. Adapters use it
// to prove a translation is complete before handing it back to the core.
func (c Capability) ValidateOutput(payload map[string]any) error {
	return checkPayload(c.ID, "output", c.Outputs, payload)
}

func checkPayload(capID, kind string, fields []Field, payload map[string]any) error {
	known := make(map[string]Field, len(fields))
	for _, f := range fields {
		known[f.Name] = f
	}
	unknown := make([]string, 0)
	for name := range payload {
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return Fail(FailureInvalidInput,
			"capability %s: unknown %s field(s): %s", capID, kind, strings.Join(unknown, ", "))
	}
	for _, f := range fields {
		value, present := payload[f.Name]
		if !present || value == nil {
			if f.Required {
				return Fail(FailureInvalidInput,
					"capability %s: %s %q is required", capID, kind, f.Name)
			}
			continue
		}
		if err := checkValue(capID, kind, f.Name, f, value); err != nil {
			return err
		}
	}
	return nil
}

func checkValue(capID, kind, path string, f Field, value any) error {
	typeErr := func() error {
		return Fail(FailureInvalidInput,
			"capability %s: %s %q must be %s, got %T", capID, kind, path, f.Type, value)
	}
	switch f.Type {
	case TypeString:
		if _, ok := value.(string); !ok {
			return typeErr()
		}
	case TypeBool:
		if _, ok := value.(bool); !ok {
			return typeErr()
		}
	case TypeInt:
		if !isInteger(value) {
			return typeErr()
		}
	case TypeStringList:
		items, ok := asList(value)
		if !ok {
			return typeErr()
		}
		for i, item := range items {
			if _, ok := item.(string); !ok {
				return Fail(FailureInvalidInput,
					"capability %s: %s %q[%d] must be string, got %T", capID, kind, path, i, item)
			}
		}
	case TypeRecord:
		record, ok := asRecord(value)
		if !ok {
			return typeErr()
		}
		return checkPayload(capID, kind+" "+path, f.Fields, record)
	case TypeRecordList:
		items, ok := asList(value)
		if !ok {
			return typeErr()
		}
		for i, item := range items {
			record, ok := asRecord(item)
			if !ok {
				return Fail(FailureInvalidInput,
					"capability %s: %s %q[%d] must be record, got %T", capID, kind, path, i, item)
			}
			if err := checkPayload(capID, fmt.Sprintf("%s %s[%d]", kind, path, i), f.Fields, record); err != nil {
				return err
			}
		}
	default:
		return Fail(FailureInvalidInput,
			"capability %s: %s %q has unknown type %d", capID, kind, path, f.Type)
	}
	return nil
}

// isInteger accepts the float64 that a JSON decoder produces for whole numbers,
// so an adapter speaking JSON is not forced to pre-convert.
func isInteger(value any) bool {
	switch n := value.(type) {
	case int, int32, int64:
		return true
	case float64:
		return n == float64(int64(n))
	default:
		return false
	}
}

func asList(value any) ([]any, bool) {
	switch list := value.(type) {
	case []any:
		return list, true
	case []string:
		out := make([]any, len(list))
		for i, s := range list {
			out[i] = s
		}
		return out, true
	case []map[string]any:
		out := make([]any, len(list))
		for i, m := range list {
			out[i] = m
		}
		return out, true
	default:
		return nil, false
	}
}

func asRecord(value any) (map[string]any, bool) {
	record, ok := value.(map[string]any)
	return record, ok
}

// Clone returns a deep copy, so handing a capability out never hands out a
// pointer into the registry.
func (c Capability) Clone() Capability {
	c.Effects = slices.Clone(c.Effects)
	c.Inputs = cloneFields(c.Inputs)
	c.Outputs = cloneFields(c.Outputs)
	return c
}

func cloneFields(fields []Field) []Field {
	if fields == nil {
		return nil
	}
	out := make([]Field, len(fields))
	for i, f := range fields {
		f.Fields = cloneFields(f.Fields)
		out[i] = f
	}
	return out
}
