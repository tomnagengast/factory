package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
)

func humanResult(content string, rawSchema json.RawMessage) (json.RawMessage, error) {
	if len(rawSchema) == 0 {
		result, err := json.Marshal(content)
		if err != nil {
			return nil, fmt.Errorf("encode comment: %w", err)
		}
		return result, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(rawSchema, &schema); err != nil {
		return nil, fmt.Errorf("gate schema is invalid: %w", err)
	}
	var value any
	if err := json.Unmarshal([]byte(content), &value); err != nil {
		return nil, errors.New("reply with valid JSON")
	}
	if err := validateSchemaValue(value, schema, "response"); err != nil {
		return nil, err
	}
	result, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode response: %w", err)
	}
	return result, nil
}

func validateSchemaValue(value any, schema map[string]any, path string) error {
	if expected, found := schema["type"].(string); found && !schemaTypeMatches(value, expected) {
		return fmt.Errorf("%s must be %s", path, expected)
	}
	if values, found := schema["enum"].([]any); found {
		matched := false
		for _, allowed := range values {
			if reflect.DeepEqual(value, allowed) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed value", path)
		}
	}
	object, isObject := value.(map[string]any)
	if isObject {
		if required, found := schema["required"].([]any); found {
			for _, field := range required {
				name, ok := field.(string)
				if _, present := object[name]; ok && !present {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, child := range object {
			childSchema, declared := properties[name].(map[string]any)
			if !declared {
				if schema["additionalProperties"] == false {
					return fmt.Errorf("%s.%s is not allowed", path, name)
				}
				continue
			}
			if err := validateSchemaValue(child, childSchema, path+"."+name); err != nil {
				return err
			}
		}
	}
	array, isArray := value.([]any)
	itemSchema, hasItems := schema["items"].(map[string]any)
	if isArray && hasItems {
		for index, item := range array {
			if err := validateSchemaValue(item, itemSchema, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func schemaTypeMatches(value any, expected string) bool {
	switch expected {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		number, ok := value.(float64)
		return ok && math.Trunc(number) == number
	case "null":
		return value == nil
	default:
		return true
	}
}
