package contract

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Effect is an observable consequence of running a capability.
//
// Read, write and external are three groups on purpose. Writing breaks
// something of your own, at home, and can be undone. Reaching outside escapes
// the machine and no undo takes it back. Putting both in the same bag would
// give the dangerous one the permissions of the harmless one.
//
// Process is a fourth, orthogonal axis: not what a capability changes, but
// whether answering it means running a binary Atenea does not fully control
// the internals of. It composes with the other three rather than replacing
// any of them -- code.search causes read AND process at once, because
// ripgrep is both.
type Effect uint8

const (
	// EffectRead touches the filesystem read-only.
	EffectRead Effect = iota
	// EffectWrite creates, edits or deletes.
	EffectWrite
	// EffectExternal leaves the machine: network, external services.
	EffectExternal
	// EffectProcess spawns an OS process to answer. ripgrep via omp, the
	// claude CLI and other external tools all cause it, each alongside
	// whichever of the other three effects that same call also causes.
	EffectProcess
)

var (
	effectNames = map[Effect]string{
		EffectRead:     "read",
		EffectWrite:    "write",
		EffectExternal: "external",
		EffectProcess:  "process",
	}
	effectByName = map[string]Effect{
		"read":     EffectRead,
		"write":    EffectWrite,
		"external": EffectExternal,
		"process":  EffectProcess,
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
	return 0, Fail(FailureInvalidInput, "unknown effect %q: want read, write, external or process", s)
}

// MarshalJSON writes an effect as its name.
//
// Without this, a list of them is a []uint8 and encoding/json writes it as
// base64: a receipt's `"effects":"AAM="` is a record nobody can audit, which
// is the only reason the field is written down. Measured on a real receipt
// before it was fixed.
func (e Effect) MarshalJSON() ([]byte, error) {
	name, ok := effectNames[e]
	if !ok {
		return nil, fmt.Errorf("effect %d has no name", uint8(e))
	}
	return json.Marshal(name)
}

// UnmarshalJSON reads either the name this writes or the bare number older
// receipts hold. The number is accepted because a run recorded before the
// name existed is still a run somebody may need to read, and refusing it
// would make an upgrade quietly lose history.
func (e *Effect) UnmarshalJSON(raw []byte) error {
	var name string
	if err := json.Unmarshal(raw, &name); err == nil {
		parsed, err := ParseEffect(name)
		if err != nil {
			return err
		}
		*e = parsed
		return nil
	}
	var number uint8
	if err := json.Unmarshal(raw, &number); err != nil {
		return fmt.Errorf("effect: want a name or a number, got %s", raw)
	}
	if _, ok := effectNames[Effect(number)]; !ok {
		return fmt.Errorf("effect: unknown number %d", number)
	}
	*e = Effect(number)
	return nil
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
	return 0, Fail(FailureInvalidInput, "unknown field type %q", s)
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
	// Enum closes a string field to a fixed set of values; empty leaves it open.
	// Declarable on string and string_list only, because a closed set of records
	// is a type, not a constraint.
	//
	// It exists for the caller that cannot be asked. A human reading a summary
	// infers that "incoming", "outgoing" or "both" is the whole list; a machine
	// generating a request from a schema cannot, and learns the boundary by
	// being refused. Naming the set is what turns a summary into a promise --
	// prose is advice, and this is checked.
	//
	// Deliberately not extended to numeric bounds: a range that lives in the
	// contract is a range every implementation must honor, and line numbers
	// are bounded by the file, not by the capability.
	Enum []string
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

// ReservedNamespace is the first segment Atenea keeps for itself.
//
// A tool reached through a declared backend rather than through a capability
// is named `raw.<server>.<tool>`, so that a caller reading a name can tell
// which of the two it is looking at without consulting anything. That only
// holds if nothing else may claim the segment: a capability called raw.search
// would be indistinguishable from a backend named search, and the funnel's
// absence would stop being visible in the name.
//
// The reservation is a refusal at definition time rather than a convention,
// because a convention is exactly what a catalog drifts away from.
const ReservedNamespace = "raw"

// firstSegment is the part before the first dot, or the whole string.
func firstSegment(id string) string {
	if i := strings.IndexByte(id, '.'); i >= 0 {
		return id[:i]
	}
	return id
}

// Validate checks the capability definition itself.
func (c Capability) Validate() error {
	if !capabilityID.MatchString(c.ID) {
		return Fail(FailureInvalidInput,
			"capability id %q must be dotted lowercase, e.g. code.search", c.ID)
	}
	if firstSegment(c.ID) == ReservedNamespace {
		return Fail(FailureInvalidInput,
			"capability id %q claims the reserved %s. namespace, which names tools reached without a funnel",
			c.ID, ReservedNamespace)
	}
	if c.Version == (Version{}) {
		return Fail(FailureInvalidInput, "%s: version is required", c.ID)
	}
	if strings.TrimSpace(c.Summary) == "" {
		return Fail(FailureInvalidInput, "%s: summary is required", c.ID)
	}
	if err := validateFields("input", "capability "+c.ID, c.Inputs); err != nil {
		return err
	}
	if err := validateFields("output", "capability "+c.ID, c.Outputs); err != nil {
		return err
	}
	for _, e := range c.Effects {
		if _, ok := effectNames[e]; !ok {
			return Fail(FailureInvalidInput, "%s: unknown effect %d", c.ID, e)
		}
	}
	return nil
}

func validateFields(kind, subject string, fields []Field) error {
	seen := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		if !fieldName.MatchString(f.Name) {
			return Fail(FailureInvalidInput,
				"%s: %s name %q must be lowercase snake_case", subject, kind, f.Name)
		}
		if _, dup := seen[f.Name]; dup {
			return Fail(FailureInvalidInput,
				"%s: duplicate %s %q", subject, kind, f.Name)
		}
		seen[f.Name] = struct{}{}
		if _, ok := typeNames[f.Type]; !ok {
			return Fail(FailureInvalidInput,
				"%s: %s %q has unknown type %d", subject, kind, f.Name, f.Type)
		}
		nested := f.Type == TypeRecord || f.Type == TypeRecordList
		switch {
		case nested && len(f.Fields) == 0:
			return Fail(FailureInvalidInput,
				"%s: %s %q is a %s and needs nested fields", subject, kind, f.Name, f.Type)
		case !nested && len(f.Fields) > 0:
			return Fail(FailureInvalidInput,
				"%s: %s %q is a %s and cannot have nested fields", subject, kind, f.Name, f.Type)
		}
		if err := validateEnum(kind, subject, f); err != nil {
			return err
		}
		if nested {
			if err := validateFields(kind, subject, f.Fields); err != nil {
				return err
			}
		}
	}
	return nil
}

// validateEnum checks an enum declaration, not a value against one.
func validateEnum(kind, subject string, f Field) error {
	if len(f.Enum) == 0 {
		return nil
	}
	if f.Type != TypeString && f.Type != TypeStringList {
		return Fail(FailureInvalidInput,
			"%s: %s %q is a %s and cannot have an enum", subject, kind, f.Name, f.Type)
	}
	seen := make(map[string]struct{}, len(f.Enum))
	for _, value := range f.Enum {
		if value == "" {
			return Fail(FailureInvalidInput,
				"%s: %s %q has an empty enum value", subject, kind, f.Name)
		}
		if _, dup := seen[value]; dup {
			return Fail(FailureInvalidInput,
				"%s: %s %q lists enum value %q twice", subject, kind, f.Name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// ValidateInput checks a payload against the declared inputs. Unknown keys are
// refused: a caller passing a field the capability never promised is a caller
// relying on one particular implementation.
func (c Capability) ValidateInput(payload map[string]any) error {
	return checkPayload("capability "+c.ID, "input", c.Inputs, payload)
}

// ValidateOutput checks a payload against the declared outputs. Adapters use it
// to prove a translation is complete before handing it back to the core.
func (c Capability) ValidateOutput(payload map[string]any) error {
	return checkPayload("capability "+c.ID, "output", c.Outputs, payload)
}

func checkPayload(subject, kind string, fields []Field, payload map[string]any) error {
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
			"%s: unknown %s field(s): %s", subject, kind, strings.Join(unknown, ", "))
	}
	for _, f := range fields {
		value, present := payload[f.Name]
		if !present || value == nil {
			if f.Required {
				return Fail(FailureInvalidInput,
					"%s: %s %q is required", subject, kind, f.Name)
			}
			continue
		}
		if err := checkValue(subject, kind, f.Name, f, value); err != nil {
			return err
		}
	}
	return nil
}

func checkValue(subject, kind, path string, f Field, value any) error {
	typeErr := func() error {
		return Fail(FailureInvalidInput,
			"%s: %s %q must be %s, got %T", subject, kind, path, f.Type, value)
	}
	switch f.Type {
	case TypeString:
		text, ok := value.(string)
		if !ok {
			return typeErr()
		}
		return checkEnum(subject, kind, path, f, text)
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
			text, ok := item.(string)
			if !ok {
				return Fail(FailureInvalidInput,
					"%s: %s %q[%d] must be string, got %T", subject, kind, path, i, item)
			}
			if err := checkEnum(subject, kind, fmt.Sprintf("%s[%d]", path, i), f, text); err != nil {
				return err
			}
		}
	case TypeRecord:
		record, ok := asRecord(value)
		if !ok {
			return typeErr()
		}
		return checkPayload(subject, kind+" "+path, f.Fields, record)
	case TypeRecordList:
		items, ok := asList(value)
		if !ok {
			return typeErr()
		}
		for i, item := range items {
			record, ok := asRecord(item)
			if !ok {
				return Fail(FailureInvalidInput,
					"%s: %s %q[%d] must be record, got %T", subject, kind, path, i, item)
			}
			if err := checkPayload(subject, fmt.Sprintf("%s %s[%d]", kind, path, i), f.Fields, record); err != nil {
				return err
			}
		}
	default:
		return Fail(FailureInvalidInput,
			"%s: %s %q has unknown type %d", subject, kind, path, f.Type)
	}
	return nil
}

// checkEnum refuses a value outside a closed set, and names the set in the
// refusal. A caller that guessed wrong needs the list, not the verdict.
func checkEnum(subject, kind, path string, f Field, value string) error {
	if len(f.Enum) == 0 || slices.Contains(f.Enum, value) {
		return nil
	}
	return Fail(FailureInvalidInput,
		"%s: %s %q must be one of %s, got %q",
		subject, kind, path, strings.Join(f.Enum, ", "), value)
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
		f.Enum = slices.Clone(f.Enum)
		out[i] = f
	}
	return out
}
