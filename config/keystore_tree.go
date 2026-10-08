package config

import (
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
	if _, isEntry := entryField(sub); isEntry {
		out[name] = sub
		return nil
	}
	if len(sub) == 0 {
		out[name] = sub
		return nil
	}
	return walkKeys(sub, name, out)
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
	fields []schemaField
}

type schemaField struct {
	name   string
	schema *treeSchema
}

func schemaOf(t reflect.Type) *treeSchema {
	s := &treeSchema{}
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
