package sealed

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/gaborage/go-bricks/jose"
)

// TagName is the struct-tag key of the sealing family. It is distinct from jose.TagName so
// jose.ScanType never sees a seal declaration and a struct cannot be both an HTTP body and
// a sealed event by accident.
const TagName = "seal"

const (
	tagKeySign     = "sign"
	tagKeyEncrypt  = "encrypt"
	tagValSubject  = "subject"
	jsonTagName    = "json"
	jsonOmitEmpty  = "omitempty"
	jsonOmitZero   = "omitzero"
	jsonSkipMarker = "-"
)

// Spec is the scanned declaration of a seal-tagged event type: the Two-kid identity from
// the sentinel and the one Subject whose json name is the signed manifest entry. It is
// immutable once built and safe to cache per type.
type Spec struct {
	// Type is the struct type scanned, pointer unwrapped. Seal refuses a value of any other type.
	Type reflect.Type
	// SignLogical and EncryptLogical are the Logical kids; the wire carries a Generation of each.
	SignLogical    string
	EncryptLogical string
	// SubjectField is the Go field name of the Subject; SubjectPath its json member name.
	SubjectField string
	SubjectPath  string

	siblingNames []string // json names of the clear members, for the case-fold check
}

// SealedPaths is the `sp` manifest: one path in v1.
func (s *Spec) SealedPaths() []string { return []string{s.SubjectPath} }

// ScanType inspects t for the `seal` tag family. It returns (nil, nil) when the type carries
// no seal tags at all, (*Spec, nil) on a valid declaration, and (nil, *jose.Error) with
// Sentinel ErrTagInvalid on any refused declaration: a malformed sentinel, a kid failing the
// Logical grammar, zero or several Subjects, a Subject without a sentinel, or a Subject that
// could vanish from the wire (embedded, unexported, `json:"-"`, omitempty, omitzero).
// Pointer types are unwrapped; non-struct types carry no tags.
func ScanType(t reflect.Type) (*Spec, error) {
	t = unwrapPointer(t)
	if t == nil || t.Kind() != reflect.Struct {
		return nil, nil
	}

	spec := &Spec{Type: t}
	sentinelSeen := false
	subjects := 0
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag, ok := field.Tag.Lookup(TagName)
		if !ok {
			if err := checkSiblingName(spec, &field); err != nil {
				return nil, err
			}
			continue
		}
		if tag == tagValSubject {
			subjects++
			if err := applySubject(spec, &field); err != nil {
				return nil, err
			}
			continue
		}
		if sentinelSeen {
			return nil, tagError(CodeTagInvalid, fmt.Sprintf("seal sentinel declared twice (second on field %s)", field.Name))
		}
		sentinelSeen = true
		if err := parseSentinel(spec, tag); err != nil {
			return nil, err
		}
	}

	switch {
	case !sentinelSeen && subjects == 0:
		return nil, nil
	case !sentinelSeen:
		return nil, tagError(CodeTagSentinelMissing, "seal:\"subject\" declared without a sentinel `_ struct{} `seal:\"sign=…,encrypt=…\"``")
	case subjects == 0:
		return nil, tagError(CodeTagSubjectMissing, "seal sentinel present but no field is tagged seal:\"subject\"")
	case subjects > 1:
		return nil, tagError(CodeTagSubjectMultiple, fmt.Sprintf("%d fields tagged seal:\"subject\"; v1 seals exactly one", subjects))
	}
	return spec, nil
}

// applySubject records the Subject and refuses any shape encoding/json could drop or rename
// away from the signed manifest: embedded (promoted members, no member of its own),
// unexported (never marshaled), `json:"-"`, and omitempty/omitzero (member absent on zero).
func applySubject(spec *Spec, field *reflect.StructField) error {
	if field.Anonymous {
		return tagError(CodeTagSubjectInvalid, fmt.Sprintf("embedded field %s cannot be the subject", field.Name))
	}
	if !field.IsExported() {
		return tagError(CodeTagSubjectInvalid, fmt.Sprintf("subject %s is unexported and would never reach the wire", field.Name))
	}
	name, opts, _ := strings.Cut(field.Tag.Get(jsonTagName), ",")
	if name == jsonSkipMarker {
		return tagError(CodeTagSubjectInvalid, fmt.Sprintf("subject %s has json:\"-\" (no wire member to pin)", field.Name))
	}
	for _, opt := range strings.Split(opts, ",") {
		if opt == jsonOmitEmpty || opt == jsonOmitZero {
			return tagError(CodeTagSubjectInvalid, fmt.Sprintf("subject %s carries json %q; the subject member must always be present", field.Name, opt))
		}
	}
	if name == "" {
		name = field.Name
	}
	spec.SubjectField = field.Name
	spec.SubjectPath = name
	return spec.checkNamesakes()
}

// checkSiblingName refuses a clear member whose json name case-folds to the Subject's:
// encoding/json matches struct fields case-insensitively on decode, so such a sibling
// would let a consumer read the clear twin instead of the sealed member. Siblings scanned
// before the Subject are remembered and judged once the Subject is known.
func checkSiblingName(spec *Spec, field *reflect.StructField) error {
	if field.Anonymous || !field.IsExported() {
		return nil
	}
	name, _, _ := strings.Cut(field.Tag.Get(jsonTagName), ",")
	if name == jsonSkipMarker {
		return nil
	}
	if name == "" {
		name = field.Name
	}
	spec.siblingNames = append(spec.siblingNames, name)
	return spec.checkNamesakes()
}

func (s *Spec) checkNamesakes() error {
	if s.SubjectPath == "" {
		return nil
	}
	for _, name := range s.siblingNames {
		if strings.EqualFold(name, s.SubjectPath) {
			return tagError(CodeTagSubjectInvalid, fmt.Sprintf("clear member %q case-folds to the subject %q; a decoder could read the clear twin", name, s.SubjectPath))
		}
	}
	return nil
}

// unwrapPointer strips pointer indirections; nil stays nil.
func unwrapPointer(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// parseSentinel parses `sign=<logical>,encrypt=<logical>`; both keys are required, each
// value must pass CheckLogicalKid.
func parseSentinel(spec *Spec, tag string) error {
	if strings.TrimSpace(tag) == "" {
		return tagError(CodeTagInvalid, "seal sentinel tag is empty")
	}
	seen := map[string]bool{}
	for _, raw := range strings.Split(tag, ",") {
		key, val, found := strings.Cut(strings.TrimSpace(raw), "=")
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !found || val == "" {
			return tagError(CodeTagInvalid, fmt.Sprintf("expected key=value in seal sentinel, got %q", raw))
		}
		if seen[key] {
			return tagError(CodeTagInvalid, fmt.Sprintf("seal sentinel key %q specified more than once", key))
		}
		seen[key] = true
		if err := applyKid(spec, key, val); err != nil {
			return err
		}
	}
	if spec.SignLogical == "" || spec.EncryptLogical == "" {
		return tagError(CodeTagInvalid, "seal sentinel needs both sign= and encrypt=")
	}
	return nil
}

func applyKid(spec *Spec, key, val string) error {
	switch key {
	case tagKeySign, tagKeyEncrypt:
	default:
		return tagError(CodeTagInvalid, fmt.Sprintf("unknown seal sentinel key %q", key))
	}
	if err := CheckLogicalKid(val); err != nil {
		return &jose.Error{
			Sentinel: ErrTagInvalid,
			Code:     CodeTagKidInvalid,
			Message:  fmt.Sprintf("logical kid %q for %s: %v", val, key, err),
			Kid:      val,
		}
	}
	if key == tagKeySign {
		spec.SignLogical = val
	} else {
		spec.EncryptLogical = val
	}
	return nil
}

func tagError(code, msg string) *jose.Error {
	return &jose.Error{Sentinel: ErrTagInvalid, Code: code, Message: msg}
}
