package contract

import "slices"

// InputSchema describes what this capability accepts, as JSON Schema.
//
// It is the declaration read the other way round. ValidateInput judges a
// payload that has already arrived; this says the same thing to a caller who
// has not sent one yet, which is the only form a far side that thinks -- or a
// client reading tools/list -- can act on.
//
// The two are deliberately generated from the same fields and kept in one
// package: a schema that advertises a shape the validator then refuses is
// worse than no schema at all, because the caller did what it was told.
func (c Capability) InputSchema() (map[string]any, error) {
	return objectSchema(c.Inputs)
}

// OutputSchema describes what this capability answers with, as JSON Schema.
//
// This is the load-bearing half of talking to a far side that thinks. A tool
// answers in whatever format it answers in and the adapter parses it; a model
// answers in whatever it was asked for, so asking precisely is the difference
// between an answer and an essay. Nothing is invented here -- it is
// transcribed.
func (c Capability) OutputSchema() (map[string]any, error) {
	return objectSchema(c.Outputs)
}

// objectSchema turns a set of declared fields into one JSON Schema object.
//
// additionalProperties is false at every depth, and that is not caution: it
// is what checkPayload already does. A payload carrying a field the capability
// never promised is refused on arrival, so a schema that allowed it would be
// advertising a door that is nailed shut.
func objectSchema(fields []Field) (map[string]any, error) {
	properties := make(map[string]any, len(fields))
	required := make([]string, 0, len(fields))
	for _, field := range fields {
		entry, err := fieldSchema(field)
		if err != nil {
			return nil, err
		}
		properties[field.Name] = entry
		if field.Required {
			required = append(required, field.Name)
		}
	}
	out := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out, nil
}

func fieldSchema(field Field) (map[string]any, error) {
	var out map[string]any
	switch field.Type {
	case TypeString:
		out = map[string]any{"type": "string"}
		enumInto(out, field)
	case TypeInt:
		out = map[string]any{"type": "integer"}
	case TypeBool:
		out = map[string]any{"type": "boolean"}
	case TypeStringList:
		items := map[string]any{"type": "string"}
		// The set constrains each element, not the list: a string_list enum
		// says which words may appear, never how many.
		enumInto(items, field)
		out = map[string]any{"type": "array", "items": items}
	case TypeRecord:
		nested, err := objectSchema(field.Fields)
		if err != nil {
			return nil, err
		}
		out = nested
	case TypeRecordList:
		nested, err := objectSchema(field.Fields)
		if err != nil {
			return nil, err
		}
		out = map[string]any{"type": "array", "items": nested}
	default:
		return nil, Fail(FailureInvalidInput,
			"field %s has a type that cannot be described as JSON Schema: %s",
			field.Name, field.Type)
	}
	if field.Summary != "" {
		// The summary is the only place the capability says what a field
		// MEANS. A far side that reads it fills the field correctly; one that
		// does not was going to guess anyway.
		out["description"] = field.Summary
	}
	return out, nil
}

// enumInto copies a declared set onto the schema node that holds the value.
//
// A copy, not the slice itself: this map is marshaled and handed out, and a
// caller that sorted it in place would be reordering the registry's own
// capability.
func enumInto(node map[string]any, field Field) {
	if len(field.Enum) == 0 {
		return
	}
	node["enum"] = slices.Clone(field.Enum)
}
