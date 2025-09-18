// Package validation provides utilities for parsing and working with validation tags
// in go-bricks applications. It offers helpers for extracting validation metadata
// from struct types that can be used for both runtime validation and documentation generation.
package validation

import (
	"reflect"
	"strconv"
	"strings"
)

const (
	trueValue = "true"
)

// TagInfo represents parsed validation tag information from a struct field
type TagInfo struct {
	Name        string            // Go field name
	JSONName    string            // JSON field name (from json tag)
	ParamType   string            // Parameter type: path, query, header, form, body
	ParamName   string            // Parameter name for path/query/header params
	Required    bool              // Whether field is required
	Constraints map[string]string // Validation constraints from validate tag
	Description string            // Documentation from doc tag
	Example     string            // Example value from example tag
	Tags        map[string]string // All struct tags for reference
}

// ParseValidationTags extracts validation metadata from a struct type
// This function analyzes struct fields and their tags to build TagInfo objects
// that contain all validation and documentation metadata.
func ParseValidationTags(t reflect.Type) []TagInfo {
	var tags []TagInfo

	// Handle pointer types
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	// Only process struct types
	if t.Kind() != reflect.Struct {
		return tags
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		tagInfo := TagInfo{
			Name:        field.Name,
			Constraints: make(map[string]string),
			Tags:        make(map[string]string),
		}

		// Parse all struct tags for reference
		parseAllStructTags(field.Tag, tagInfo.Tags)

		// Parse JSON tag for field naming
		if json := field.Tag.Get("json"); json != "" {
			parts := strings.Split(json, ",")
			if parts[0] == "-" {
				tagInfo.JSONName = "-"
			} else if parts[0] != "" {
				tagInfo.JSONName = parts[0]
			}
			// Check for omitempty which affects required status
			for _, part := range parts[1:] {
				if strings.TrimSpace(part) == "omitempty" {
					tagInfo.Tags["omitempty"] = trueValue
				}
			}
		}

		// Determine parameter type and name
		tagInfo.ParamType, tagInfo.ParamName = parseParameterInfo(field.Tag)

		// Parse validation constraints
		if validate := field.Tag.Get("validate"); validate != "" {
			parseValidateTag(validate, tagInfo.Constraints)
		}

		// Determine if field is required
		tagInfo.Required = isFieldRequired(tagInfo.Constraints, tagInfo.Tags, tagInfo.ParamType, tagInfo.JSONName)

		// Parse documentation tags
		if doc := field.Tag.Get("doc"); doc != "" {
			tagInfo.Description = doc
		} else if description := field.Tag.Get("description"); description != "" {
			tagInfo.Description = description
		}

		if example := field.Tag.Get("example"); example != "" {
			tagInfo.Example = example
		}

		tags = append(tags, tagInfo)
	}

	return tags
}

// parseAllStructTags parses all struct tags into a map
func parseAllStructTags(tag reflect.StructTag, tags map[string]string) {
	for _, k := range []string{
		"json", "validate", "doc", "description", "example",
		"param", "query", "header", "form",
	} {
		if v := tag.Get(k); v != "" {
			tags[k] = v
		}
	}
}

// parseParameterInfo determines parameter type and name from struct tags
func parseParameterInfo(tag reflect.StructTag) (paramType, paramName string) {
	// Check for path parameter tag
	if param := tag.Get("param"); param != "" {
		return "path", param
	}

	// Check for query parameter tag
	if query := tag.Get("query"); query != "" {
		return "query", query
	}

	// Check for header tag
	if header := tag.Get("header"); header != "" {
		return "header", header
	}

	// Check for form tag (for form data)
	if form := tag.Get("form"); form != "" {
		return "form", form
	}

	// Default to body parameter
	return "body", ""
}

// parseValidateTag parses a validate tag into constraint map
func parseValidateTag(validate string, constraints map[string]string) {
	// Split by comma to get individual constraints
	parts := strings.Split(validate, ",")

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		// Handle simple flags like "required"
		if !strings.Contains(part, "=") {
			constraints[part] = trueValue
			continue
		}

		// Handle key=value constraints like "min=1", "max=100"
		kv := strings.SplitN(part, "=", 2)
		if len(kv) == 2 {
			key := strings.TrimSpace(kv[0])
			value := strings.TrimSpace(kv[1])
			value = strings.Trim(value, `"`)
			constraints[key] = value
		}
	}
}

// isFieldRequired determines if a field is required based on validation and other tags
func isFieldRequired(constraints, tags map[string]string, paramType, jsonName string) bool {
	// Respect explicit omissions from validation or JSON tags
	if _, skip := constraints["omitempty"]; skip {
		return false
	}
	if _, skip := tags["omitempty"]; skip {
		return false
	}

	// Explicit required constraint
	if _, required := constraints["required"]; required {
		return true
	}

	// Path parameters are always required
	if paramType == "path" {
		return true
	}

	// JSON fields without omitempty are typically required for body params
	if paramType == "body" {
		if _, hasOmitempty := tags["omitempty"]; !hasOmitempty {
			// Empty jsonName means default (present); "-" means explicitly ignored.
			if jsonName != "-" {
				return true
			}
		}
	}

	return false
}

// Constraint getter methods for common validation rules

// IsRequired returns true if the field has a required constraint
func (t *TagInfo) IsRequired() bool {
	return t.Required
}

// GetMin returns the minimum value constraint if present
func (t *TagInfo) GetMin() (int, bool) {
	if minVal, ok := t.Constraints["min"]; ok {
		if val, err := strconv.Atoi(minVal); err == nil {
			return val, true
		}
	}
	return 0, false
}

// GetMax returns the maximum value constraint if present
func (t *TagInfo) GetMax() (int, bool) {
	if maxVal, ok := t.Constraints["max"]; ok {
		if val, err := strconv.Atoi(maxVal); err == nil {
			return val, true
		}
	}
	return 0, false
}

// GetMinLength returns the minimum length constraint if present
func (t *TagInfo) GetMinLength() (int, bool) {
	if minLen, ok := t.Constraints["min_len"]; ok {
		if val, err := strconv.Atoi(minLen); err == nil {
			return val, true
		}
	}
	return 0, false
}

// GetMaxLength returns the maximum length constraint if present
func (t *TagInfo) GetMaxLength() (int, bool) {
	if maxLen, ok := t.Constraints["max_len"]; ok {
		if val, err := strconv.Atoi(maxLen); err == nil {
			return val, true
		}
	}
	return 0, false
}

// GetPattern returns the regex pattern constraint if present
func (t *TagInfo) GetPattern() (string, bool) {
	pattern, ok := t.Constraints["regexp"]
	return pattern, ok
}

// GetEnum returns enum values if present
func (t *TagInfo) GetEnum() ([]string, bool) {
	if enum, ok := t.Constraints["oneof"]; ok {
		values := strings.Fields(enum)
		if len(values) > 0 {
			return values, true
		}
	}
	return nil, false
}

// HasFormat returns true if the field has a specific format constraint
func (t *TagInfo) HasFormat(format string) bool {
	constraint, exists := t.Constraints[format]
	return exists && constraint == trueValue
}

// IsEmail returns true if the field has email validation
func (t *TagInfo) IsEmail() bool {
	return t.HasFormat("email")
}

// IsURL returns true if the field has URL validation
func (t *TagInfo) IsURL() bool {
	return t.HasFormat("url")
}

// IsUUID returns true if the field has UUID validation
func (t *TagInfo) IsUUID() bool {
	return t.HasFormat("uuid")
}

// GetConstraints returns all validation constraints
func (t *TagInfo) GetConstraints() map[string]string {
	result := make(map[string]string, len(t.Constraints))
	for k, v := range t.Constraints {
		result[k] = v
	}
	return result
}
