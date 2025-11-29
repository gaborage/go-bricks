package validation

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Test struct for validation tag parsing
type TestRequest struct {
	ID       int    `param:"id" validate:"required,min=1" doc:"User ID" example:"123"`
	Name     string `json:"name" validate:"required,min=2,max=100" doc:"User's full name" example:"John Doe"`
	Email    string `json:"email" validate:"required,email" doc:"User's email address" example:"john.doe@example.com"`
	Age      *int   `json:"age,omitempty" validate:"omitempty,min=13,max=120" doc:"User's age (optional)" example:"30"`
	Role     string `json:"role" validate:"oneof=admin user guest" doc:"User role" example:"user"`
	Active   bool   `json:"active" doc:"Whether the user account is active" example:"true"`
	Optional string `json:"optional,omitempty" doc:"Optional field"`
}

func TestParseValidationTags(t *testing.T) {
	tags := ParseValidationTags(reflect.TypeOf(TestRequest{}))

	assert.Len(t, tags, 7, "Should parse all 7 fields")

	// Test ID field (path parameter)
	idTag := findTagByName(tags, "ID")
	assert.NotNil(t, idTag, "ID field should be found")
	assert.Equal(t, "path", idTag.ParamType)
	assert.Equal(t, "id", idTag.ParamName)
	assert.True(t, idTag.Required)
	assert.Equal(t, "User ID", idTag.Description)
	assert.Equal(t, "123", idTag.Example)

	minVal, hasMin := idTag.Min()
	assert.True(t, hasMin)
	assert.Equal(t, 1, minVal)

	// Test Name field (JSON with constraints)
	nameTag := findTagByName(tags, "Name")
	assert.NotNil(t, nameTag, "Name field should be found")
	assert.Equal(t, "body", nameTag.ParamType)
	assert.Equal(t, "name", nameTag.JSONName)
	assert.True(t, nameTag.Required)
	assert.Equal(t, "User's full name", nameTag.Description)

	minLen, hasMinLen := nameTag.Min()
	assert.True(t, hasMinLen)
	assert.Equal(t, 2, minLen)

	maxLen, hasMaxLen := nameTag.Max()
	assert.True(t, hasMaxLen)
	assert.Equal(t, 100, maxLen)

	// Test Email field (format validation)
	emailTag := findTagByName(tags, "Email")
	assert.NotNil(t, emailTag, "Email field should be found")
	assert.True(t, emailTag.IsEmail())
	assert.True(t, emailTag.Required)

	// Test Age field (optional with omitempty)
	ageTag := findTagByName(tags, "Age")
	assert.NotNil(t, ageTag, "Age field should be found")
	assert.False(t, ageTag.Required, "Age should not be required due to omitempty")
	assert.Equal(t, "age", ageTag.JSONName)

	// Test Role field (enum validation)
	roleTag := findTagByName(tags, "Role")
	assert.NotNil(t, roleTag, "Role field should be found")
	enum, hasEnum := roleTag.Enum()
	assert.True(t, hasEnum)
	assert.Equal(t, []string{"admin", "user", "guest"}, enum)

	// Test Optional field (no validation constraints)
	optionalTag := findTagByName(tags, "Optional")
	assert.NotNil(t, optionalTag, "Optional field should be found")
	assert.False(t, optionalTag.Required)
	assert.Equal(t, "optional", optionalTag.JSONName)
}

func TestConstraintMethods(t *testing.T) {
	// Test with a field that has various constraints
	type TestStruct struct {
		Field string `validate:"required,min=5,max=20,email,regexp=^[a-z]+$" doc:"Test field" example:"test"`
	}

	tags := ParseValidationTags(reflect.TypeOf(TestStruct{}))
	assert.Len(t, tags, 1)

	tag := tags[0]
	assert.True(t, tag.IsRequired())
	assert.True(t, tag.IsEmail())

	minVal, hasMin := tag.Min()
	assert.True(t, hasMin)
	assert.Equal(t, 5, minVal)

	maxVal, hasMax := tag.Max()
	assert.True(t, hasMax)
	assert.Equal(t, 20, maxVal)

	pattern, hasPattern := tag.Pattern()
	assert.True(t, hasPattern)
	assert.Equal(t, "^[a-z]+$", pattern)
}

func TestParameterTypes(t *testing.T) {
	type TestStruct struct {
		PathParam   int    `param:"id"`
		QueryParam  string `query:"search"`
		HeaderParam string `header:"X-Custom-Header"`
		FormParam   string `form:"upload"`
		BodyParam   string `json:"name"`
	}

	tags := ParseValidationTags(reflect.TypeOf(TestStruct{}))
	assert.Len(t, tags, 5)

	pathTag := findTagByName(tags, "PathParam")
	assert.Equal(t, "path", pathTag.ParamType)
	assert.Equal(t, "id", pathTag.ParamName)

	queryTag := findTagByName(tags, "QueryParam")
	assert.Equal(t, "query", queryTag.ParamType)
	assert.Equal(t, "search", queryTag.ParamName)

	headerTag := findTagByName(tags, "HeaderParam")
	assert.Equal(t, "header", headerTag.ParamType)
	assert.Equal(t, "X-Custom-Header", headerTag.ParamName)

	formTag := findTagByName(tags, "FormParam")
	assert.Equal(t, "form", formTag.ParamType)
	assert.Equal(t, "upload", formTag.ParamName)

	bodyTag := findTagByName(tags, "BodyParam")
	assert.Equal(t, "body", bodyTag.ParamType)
	assert.Equal(t, "name", bodyTag.JSONName)
}

func TestParseValidationTagsPointerAndUnexported(t *testing.T) {
	type embedded struct {
		Exported string `json:"exported" validate:"required"`
		hidden   string
	}

	_ = embedded{hidden: "ignored"}

	tags := ParseValidationTags(reflect.TypeOf(&embedded{}))
	assert.Len(t, tags, 1)
	assert.Equal(t, "Exported", tags[0].Name)
	assert.True(t, tags[0].Required)
}

func TestParseValidationTagsNonStruct(t *testing.T) {
	tags := ParseValidationTags(reflect.TypeOf(42))
	assert.Empty(t, tags)
}

func TestTagInfoAdditionalAccessors(t *testing.T) {
	type AccessorStruct struct {
		Field string `json:"field,omitempty" validate:"min_len=3,max_len=10,url,uuid"`
	}

	tags := ParseValidationTags(reflect.TypeOf(AccessorStruct{}))
	assert.Len(t, tags, 1)
	tag := tags[0]

	minLen, ok := tag.MinLength()
	assert.True(t, ok)
	assert.Equal(t, 3, minLen)

	maxLen, ok := tag.MaxLength()
	assert.True(t, ok)
	assert.Equal(t, 10, maxLen)

	assert.True(t, tag.IsURL())
	assert.True(t, tag.HasFormat("url"))
	assert.True(t, tag.IsUUID())
	assert.False(t, tag.HasFormat("ipv4"))

	constraints := tag.AllConstraints()
	assert.Equal(t, "3", constraints["min_len"])

	constraints["min_len"] = "100"
	assert.Equal(t, "3", tag.Constraints["min_len"], "original constraints should not change")
}

func TestParseValidateTagIgnoresEmptySegments(t *testing.T) {
	constraints := make(map[string]string)
	parseValidateTag("required,,min=3, ,max=10", constraints)

	assert.Equal(t, "true", constraints["required"])
	assert.Equal(t, "3", constraints["min"])
	assert.Equal(t, "10", constraints["max"])
	_, exists := constraints[""]
	assert.False(t, exists)
}

func TestTagInfoNumericConstraintsInvalid(t *testing.T) {
	tag := &TagInfo{Constraints: map[string]string{
		"min":     "not-a-number",
		"max":     "",
		"min_len": "abc",
		"max_len": "xyz",
	}}

	_, ok := tag.Min()
	assert.False(t, ok)

	_, ok = tag.Max()
	assert.False(t, ok)

	_, ok = tag.MinLength()
	assert.False(t, ok)

	_, ok = tag.MaxLength()
	assert.False(t, ok)
}

func TestTagInfoEnumMissing(t *testing.T) {
	tag := &TagInfo{Constraints: map[string]string{}}
	values, ok := tag.Enum()
	assert.False(t, ok)
	assert.Nil(t, values)

	tag.Constraints["oneof"] = ""
	values, ok = tag.Enum()
	assert.False(t, ok)
	assert.Nil(t, values)
}

// Helper function to find a tag by field name
func findTagByName(tags []TagInfo, name string) *TagInfo {
	for _, tag := range tags {
		if tag.Name == name {
			return &tag
		}
	}
	return nil
}
