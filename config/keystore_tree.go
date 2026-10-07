package config

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/go-viper/mapstructure/v2"

	"github.com/gaborage/go-bricks/internal/keyname"
)

// A keystore.keys entry name is a dotted path of segments (ADR-144). YAML
// writes it as nested maps and the environment reaches the same path through
// the unchanged transform: KEYSTORE_KEYS_TOKENS_OUR_PRIVATE_VALUE and
// keystore.keys: {tokens: {our: {private: {value: …}}}} are one koanf path, so
// they are one entry, "tokens.our". mapstructure would decode that path into
// a phantom entry "tokens" and drop "our" in silence (ErrorUnused is off), so
// keystoreTreeHook reads the nested subtree into a flat map keyed by dotted
// names first, and mapstructure decodes that map exactly as before.
//
// The walk lives in decode because decode is the only place the nested tree
// exists. The rules that judge the final name set live in checkKeyStore, so a
// hand-built Config meets them too (ADR-064).

var (
	keysMapType = reflect.TypeFor[map[string]KeyPairConfig]()
	// keyEntrySchema is the key tree one keystore entry may hold, read from the
	// mapstructure tags of KeyPairConfig and its source structs, so a field
	// added there is accepted here without a second list.
	keyEntrySchema = schemaOf(reflect.TypeFor[KeyPairConfig]())
)

// keystoreTreeHook is the decode hook that reads the keystore.keys subtree as
// dotted names. mapstructure runs it at every nested decode, so it fires
// wherever the target is the entry map (map[string]KeyPairConfig): Load,
// LoadFromMap and Config.Unmarshal of the keystore section or of its keys map
// alike, and no exported type changes. Every refusal is a *ConfigError;
// mapstructure wraps it in a DecodeError that unwraps, so errors.As reaches it.
func keystoreTreeHook() mapstructure.DecodeHookFuncType {
	return func(_, to reflect.Type, data any) (any, error) {
		if to == keysMapType {
			return readKeysNode(data)
		}
		return data, nil
	}
}

// readKeysNode reads the node an entry map is decoded from. Anything that is
// not a string-keyed map is passed through for mapstructure to judge, as
// before.
func readKeysNode(data any) (any, error) {
	tree, isMap := stringMap(data)
	if !isMap {
		return data, nil
	}
	return readKeyTree(tree)
}

// readKeyTree flattens keystore.keys: every node with a field child (public,
// private, secret, pkcs12) is an entry named by its path; every other map is a
// namespace whose children are further name segments.
func readKeyTree(tree map[string]any) (map[string]any, error) {
	out := make(map[string]any)
	if err := walkKeys(tree, "", out); err != nil {
		return nil, err
	}
	return out, nil
}

func walkKeys(node map[string]any, prefix string, out map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(node)) {
		if err := checkTreeKey(fieldKeystoreKeys, prefix, key); err != nil {
			return err
		}
		if err := readKeyNode(joinName(prefix, key), node[key], out); err != nil {
			return err
		}
	}
	return nil
}

// readKeyNode reads the node at name: an entry (recorded in out), a namespace
// (walked), or a refusal.
func readKeyNode(name string, child any, out map[string]any) error {
	switch child.(type) {
	case KeyPairConfig, *KeyPairConfig:
		// A typed entry (LoadFromMap given map[string]KeyPairConfig) is an entry as it
		// stands; mapstructure assigns it directly, as it did before the walk.
		out[name] = child
		return nil
	}
	if child == nil {
		// A null entry reads as an empty one: an entry with no fields, so the
		// source check still reports what it lacks.
		child = map[string]any{}
	}
	sub, isMap := stringMap(child)
	if !isMap {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldKeystoreKeys + "." + name,
			Message:  "holds a value where an entry or a further name segment was expected",
			Action:   "give the entry its fields (" + fieldList(keyEntrySchema) + "), or remove the value",
		}
	}
	if field, isEntry := entryField(sub); isEntry {
		if err := checkEntry(name, field, sub); err != nil {
			return err
		}
		out[name] = sub
		return nil
	}
	if len(sub) == 0 {
		out[name] = sub
		return nil
	}
	return walkKeys(sub, name, out)
}

// checkEntry holds an entry's children to its fields and each field's subtree
// to its struct tags. A child that is no field is refused: as a prefix
// conflict when its subtree holds an entry, as an unknown field otherwise.
func checkEntry(name, setField string, entry map[string]any) error {
	for _, key := range slices.Sorted(maps.Keys(entry)) {
		if fieldSchema := keyEntrySchema.child(key); fieldSchema != nil {
			if err := checkAgainstSchema(fieldKeystoreKeys+"."+name+"."+key, key, entry[key], fieldSchema); err != nil {
				return err
			}
			continue
		}
		if nested, found := firstEntryUnder(entry[key], joinName(name, key)); found {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    fieldKeystoreKeys + "." + name,
				Message:  fmt.Sprintf("%q is an entry (it sets %s) and the parent of entry %q", name, setField, nested),
				Action:   fmt.Sprintf("rename one of them (%s → %s.default) so no entry name is a prefix of another", name, name),
			}
		}
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    fieldKeystoreKeys + "." + name + "." + key,
			Message:  fmt.Sprintf("unknown field %q in entry %q", key, name),
			Action:   "an entry takes " + fieldList(keyEntrySchema),
		}
	}
	return nil
}

// checkAgainstSchema refuses a key the struct tags do not name, anywhere below
// an entry's field, which turns ErrorUnused off for this subtree only. A null
// node is left for the source checks; a value of the wrong shape is refused
// here, so the error names the path rather than mapstructure's type.
func checkAgainstSchema(path, label string, node any, s *treeSchema) error {
	if node == nil || s.typed(node) {
		return nil
	}
	sub, isMap := stringMap(node)
	if s.leaf() {
		if isMap {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    path,
				Message:  "holds a map where a value was expected",
				Action:   "set " + label + " to a single value",
			}
		}
		return nil
	}
	if !isMap {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    path,
			Message:  "holds a single value where a map was expected",
			Action:   label + " takes " + fieldList(s),
		}
	}
	for _, key := range slices.Sorted(maps.Keys(sub)) {
		childSchema := s.child(key)
		if childSchema == nil {
			return &ConfigError{
				Category: errCategoryInvalid,
				Field:    path + "." + key,
				Message:  fmt.Sprintf("unknown field %q", key),
				Action:   label + " takes " + fieldList(s),
			}
		}
		if err := checkAgainstSchema(path+"."+key, key, sub[key], childSchema); err != nil {
			return err
		}
	}
	return nil
}

// checkTreeKey refuses a key the walk cannot read as one name segment: an
// empty key, and a key that itself contains '.' (a quoted YAML key, or a
// literal key in a LoadFromMap tree). koanf keeps such a key as a node apart
// from the nested path an environment variable reaches, so one name would
// split its precedence across two nodes.
func checkTreeKey(root, prefix, key string) error {
	parent := root
	if prefix != "" {
		parent = root + "." + prefix
	}
	if key == "" {
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    parent,
			Message:  "holds an empty key",
			Action:   "remove it, or give it a name",
		}
	}
	if !strings.Contains(key, keyname.Sep) {
		return nil
	}
	if hasEmptySegment(key) {
		// No nested form or variable spells it: both would hold an empty key.
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    parent,
			Message:  fmt.Sprintf("key %q has an empty segment", key),
			Action:   "rename it with non-empty segments, written as nested keys",
		}
	}
	if word, reserved := keyname.ReservedAfterDot(joinName(prefix, key)); reserved && root == fieldKeystoreKeys {
		// The nested form would read the word as the entry's field, so only a rename works.
		return &ConfigError{
			Category: errCategoryInvalid,
			Field:    parent,
			Message:  fmt.Sprintf("key %q is one YAML key containing '.'", key),
			Action: fmt.Sprintf("rename it (e.g. %s, or another segment than %s): nested under a name, %s reads as that entry's field",
				strings.ReplaceAll(joinName(prefix, key), keyname.Sep+word, "-"+word), word, word),
		}
	}
	action := fmt.Sprintf("write it nested (%s)", nestedForm(key))
	if envVar := envVarForKey(root + "." + joinName(prefix, key)); envVar != "" {
		action += fmt.Sprintf("; the nested path is what %s reaches", envReach(root, envVar))
	}
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    parent,
		Message:  fmt.Sprintf("key %q is one YAML key containing '.'", key),
		Action:   action,
	}
}

// envReach names the variables that reach a name under root: a keystore
// entry holds its fields and sources below the name (KEYSTORE_KEYS_TOKENS_OUR_*),
// while a selector is the leaf itself, reached by exactly one variable.
func envReach(root, envVar string) string {
	if root == fieldKeystoreKeys {
		return envVar + "_*"
	}
	return envVar
}

// entryField reports the first child (in sorted order) that names an entry
// field, which makes the node an entry.
func entryField(node map[string]any) (string, bool) {
	for _, key := range slices.Sorted(maps.Keys(node)) {
		if keyEntrySchema.child(key) != nil {
			return key, true
		}
	}
	return "", false
}

// firstEntryUnder reports the first entry name (in sorted depth-first order)
// in the subtree node, which sits at name.
func firstEntryUnder(node any, name string) (string, bool) {
	sub, isMap := stringMap(node)
	if !isMap {
		return "", false
	}
	if _, isEntry := entryField(sub); isEntry {
		return name, true
	}
	for _, key := range slices.Sorted(maps.Keys(sub)) {
		if nested, found := firstEntryUnder(sub[key], joinName(name, key)); found {
			return nested, true
		}
	}
	return "", false
}

// nestedForm renders a dotted key as the nested YAML that spells it:
// "tokens.our" becomes "tokens: {our: …}".
func nestedForm(key string) string {
	segs := strings.Split(key, keyname.Sep)
	return strings.Join(segs, ": {") + ": …" + strings.Repeat("}", len(segs)-1)
}

func joinName(prefix, segment string) string {
	if prefix == "" {
		return segment
	}
	return prefix + keyname.Sep + segment
}

// stringMap returns node as a map[string]any when it is a map with string
// keys, converting another string-keyed map type without touching it.
func stringMap(node any) (map[string]any, bool) {
	if m, ok := node.(map[string]any); ok {
		return m, true
	}
	v := reflect.ValueOf(node)
	if v.Kind() != reflect.Map || v.Type().Key().Kind() != reflect.String {
		return nil, false
	}
	out := make(map[string]any, v.Len())
	for iter := v.MapRange(); iter.Next(); {
		out[iter.Key().String()] = iter.Value().Interface()
	}
	return out, true
}

// treeSchema is the key tree a struct accepts: one field per mapstructure
// tag, in declaration order; a non-struct field is a leaf with none.
type treeSchema struct {
	typ    reflect.Type
	fields []schemaField
}

type schemaField struct {
	name   string
	schema *treeSchema
}

func schemaOf(t reflect.Type) *treeSchema {
	s := &treeSchema{typ: t}
	if t.Kind() != reflect.Struct {
		return s
	}
	for field := range t.Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("mapstructure"), ",")
		if name == "" || name == "-" {
			continue
		}
		s.fields = append(s.fields, schemaField{name: name, schema: schemaOf(field.Type)})
	}
	return s
}

func (s *treeSchema) leaf() bool { return len(s.fields) == 0 }

// typed reports whether node already is the field's Go type, or a pointer to it: a
// LoadFromMap caller may hand over a typed source, which the decoder accepts as is.
func (s *treeSchema) typed(node any) bool {
	t := reflect.TypeOf(node)
	return t == s.typ || (t.Kind() == reflect.Pointer && t.Elem() == s.typ)
}

// child returns the schema of key, matched as mapstructure matches a field
// name (case-insensitively), or nil when the struct has no such field.
func (s *treeSchema) child(key string) *treeSchema {
	for _, f := range s.fields {
		if strings.EqualFold(f.name, key) {
			return f.schema
		}
	}
	return nil
}

// names returns the tag names, in declaration order.
func (s *treeSchema) names() []string {
	names := make([]string, len(s.fields))
	for i, f := range s.fields {
		names[i] = f.name
	}
	return names
}

// fieldList renders the schema's field names for a message: "file or value",
// "public, private, secret or pkcs12".
func fieldList(s *treeSchema) string {
	names := s.names()
	switch n := len(names); n {
	case 0:
		return ""
	case 1:
		return names[0]
	default:
		return strings.Join(names[:n-1], ", ") + " or " + names[n-1]
	}
}
