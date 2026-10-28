package config

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/gaborage/go-bricks/internal/keyname"
)

// caseTwin is one group of sibling keys that fold to one key: their dotted paths, sorted.
// inSequence marks a group found in a map inside a sequence, whose paths carry an element
// index that no environment variable and no presence record reaches.
type caseTwin struct {
	paths      []string
	inSequence bool
}

// checkKeySpelling refuses a merged tree that decode would read under a spelling the
// operator did not choose (ADR-157). mapstructure matches a key to its field
// case-insensitively, so a root key that folds to a framework section name decodes into
// that section, while Config.Exists and the observability bootstrap look the section up by
// its exact name and read it as absent. Such a key is refused unless it is spelled as the
// section's name. And mapstructure takes a key's exact spelling first, otherwise the first
// case-insensitive match a Go map yields, so of two sibling keys that differ only by case
// one is dropped without a word, possibly a different one per run: no map node may hold
// two keys that fold to one, wherever each came from. It renders key names only, never a
// value.
func (s *configSource) checkKeySpelling() error {
	root := s.k.Raw()
	errs := misspelledSections(root)
	var twins []caseTwin
	collectCaseTwins(root, "", false, &twins)
	for _, twin := range twins {
		errs = append(errs, s.caseTwinError(twin))
	}
	return errors.Join(errs...)
}

// misspelledSections returns one error per framework section that a root key folds to
// without being spelled as it, every such spelling listed.
func misspelledSections(root map[string]any) []error {
	wrong := map[string][]string{}
	for key := range root {
		if section := sectionNamed(key); section != "" && key != section {
			wrong[section] = append(wrong[section], key)
		}
	}
	errs := make([]error, 0, len(wrong))
	for _, section := range slices.Sorted(maps.Keys(wrong)) {
		spellings := strings.Join(slices.Sorted(slices.Values(wrong[section])), ", ")
		errs = append(errs, &ConfigError{
			Category: errCategoryInvalid,
			Field:    section,
			Message:  "is spelled " + spellings + ";",
			Action:   "framework section names are lowercase: rename " + spellings + " to " + section,
		})
	}
	return errs
}

// sectionNamed returns the framework section key folds to, or "" for none. Section names
// never fold to each other, so at most one matches.
func sectionNamed(key string) string {
	for section := range configSections {
		if sameSegment(key, section) {
			return section
		}
	}
	return ""
}

// collectCaseTwins appends every group of two or more sibling keys in node that fold to one
// key, depth first in sorted key order so the groups come out the same on every run. A root
// group that folds to a section name is misspelledSections' to report.
func collectCaseTwins(node any, prefix string, inSequence bool, twins *[]caseTwin) {
	if isSequence(node) {
		seq := reflect.ValueOf(node)
		for i := range seq.Len() {
			collectCaseTwins(seq.Index(i).Interface(), prefix+"["+strconv.Itoa(i)+"]", true, twins)
		}
		return
	}
	tree, isMap := stringMap(node)
	if !isMap {
		return
	}
	keys := slices.Sorted(maps.Keys(tree))
	for _, group := range foldGroups(keys) {
		if len(group) < 2 || (prefix == "" && sectionNamed(group[0]) != "") {
			continue
		}
		paths := make([]string, len(group))
		for i, key := range group {
			paths[i] = joinPath(prefix, key)
		}
		*twins = append(*twins, caseTwin{paths: paths, inSequence: inSequence})
	}
	for _, key := range keys {
		collectCaseTwins(tree[key], joinPath(prefix, key), inSequence, twins)
	}
}

// foldGroups partitions sorted sibling keys into the groups sameSegment folds together,
// each group keeping the given order.
func foldGroups(keys []string) [][]string {
	var groups [][]string
	for _, key := range keys {
		i := slices.IndexFunc(groups, func(group []string) bool { return sameSegment(group[0], key) })
		if i < 0 {
			groups = append(groups, []string{key})
			continue
		}
		groups[i] = append(groups[i], key)
	}
	return groups
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + koanfDelim + key
}

// caseTwinError names every spelling of a twin, marks the ones the framework's defaults
// put there, and points at the one spelling to keep with the environment variable that
// reaches it.
func (s *configSource) caseTwinError(twin caseTwin) error {
	field := lowercaseKey(twin.paths[0])
	listed := make([]string, len(twin.paths))
	for i, path := range twin.paths {
		listed[i] = path
		if !twin.inSequence && s.fromDefaults(path) {
			listed[i] += " (framework default)"
		}
	}
	keep := field
	if envVar := envVarForKey(field); envVar != "" && !twin.inSequence {
		keep += " (" + envVar + ")"
	}
	return &ConfigError{
		Category: errCategoryInvalid,
		Field:    field,
		Message:  "is written in spellings that differ only by case: " + strings.Join(listed, ", ") + ";",
		Action:   "keys are lowercase: write it once as " + keep,
	}
}

// fromDefaults reports whether path reached the tree from the framework's preloaded
// defaults: no operator layer delivered that exact spelling or anything below it, and the
// defaults are the only layer presence never records (ADR-104). It asks the key lookup
// through a Config view of the source, since decode has not built the Config yet.
func (s *configSource) fromDefaults(path string) bool {
	view := Config{src: s}
	return !slices.ContainsFunc(view.deliveries(path, true), func(d keyDelivery) bool {
		return d.path == path || keyname.IsDottedPrefix(path, d.path)
	})
}

// lowercaseKey is the documented spelling of a folded path, for messages only: upper then
// lower case maps every simple fold onto one letter, so U+017F reads as s. What folds
// together is sameSegment's to decide, never this.
func lowercaseKey(path string) string {
	return strings.ToLower(strings.ToUpper(path))
}
