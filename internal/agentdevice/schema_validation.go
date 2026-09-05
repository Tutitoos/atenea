package agentdevice

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

// validateSchema covers the vocabulary of the pinned click/wait schemas. The
// fingerprint gate runs first, so a new upstream vocabulary cannot be guessed.
func validateSchema(schema map[string]any, value any, path string) error {
	if choices, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, choice := range choices {
			if validateSchema(choice.(map[string]any), value, path) == nil {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("%s must match exactly one target variant", path)
		}
	}
	if constant, exists := schema["const"]; exists && !reflect.DeepEqual(constant, value) {
		return fmt.Errorf("%s has the wrong discriminator", path)
	}
	if choices, ok := schema["enum"].([]any); ok {
		found := false
		for _, choice := range choices {
			if reflect.DeepEqual(choice, value) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("%s is outside the supported values", path)
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be an object", path)
		}
		properties, _ := schema["properties"].(map[string]any)
		required, _ := schema["required"].([]any)
		for _, key := range required {
			if _, ok := object[key.(string)]; !ok {
				return fmt.Errorf("%s.%s is required", path, key)
			}
		}
		for key, child := range object {
			property, known := properties[key]
			if !known {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s.%s is not supported", path, key)
				}
				continue
			}
			if err := validateSchema(property.(map[string]any), child, path+"."+key); err != nil {
				return err
			}
		}
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("%s must be a string", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be a boolean", path)
		}
	case "number", "integer":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || (schema["type"] == "integer" && math.Trunc(number) != number) {
			return fmt.Errorf("%s must be a finite %s", path, schema["type"])
		}
		if minimum, ok := schema["minimum"].(float64); ok && number < minimum {
			return fmt.Errorf("%s must be at least %g", path, minimum)
		}
	}
	return nil
}

func validatePinnedSchema(raw json.RawMessage, args map[string]any) error {
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return err
	}
	return validateSchema(schema, args, "arguments")
}
