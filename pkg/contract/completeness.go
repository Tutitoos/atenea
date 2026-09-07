package contract

import (
	"fmt"
	"strings"
)

// StructuralPartial reports payload markers that make a result unsuitable
// for a complete reusable answer. Cache admission and quality observation use
// this same predicate so inconsistent Evidence cannot change classification.
func StructuralPartial(value any) bool {
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "truncated", "partial", "incomplete", "lower_bound":
				if structuralFlag(child) {
					return true
				}
			case "source_trimmed", "trimmed", "truncated_count", "omitted":
				if positiveNumber(child) {
					return true
				}
			case "next_cursor", "continuation_cursor":
				if text := strings.TrimSpace(fmt.Sprint(child)); text != "" && text != "<nil>" {
					return true
				}
			case "completeness":
				if text, ok := child.(string); ok && text != "" && !strings.EqualFold(strings.TrimSpace(text), "complete") {
					return true
				}
			}
			if StructuralPartial(child) {
				return true
			}
		}
	case []any:
		for _, child := range v {
			if StructuralPartial(child) {
				return true
			}
		}
	}
	return false
}

func structuralFlag(value any) bool {
	switch v := value.(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "partial", "incomplete", "lower_bound", "truncated":
			return true
		}
	}
	return positiveNumber(value)
}

func positiveNumber(value any) bool {
	switch n := value.(type) {
	case int:
		return n > 0
	case int8:
		return n > 0
	case int16:
		return n > 0
	case int32:
		return n > 0
	case int64:
		return n > 0
	case uint:
		return n > 0
	case uint8:
		return n > 0
	case uint16:
		return n > 0
	case uint32:
		return n > 0
	case uint64:
		return n > 0
	case float32:
		return n > 0
	case float64:
		return n > 0
	}
	return false
}
