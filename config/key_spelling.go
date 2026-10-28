package config

import (
	"errors"
	"maps"
	"slices"
	"strings"
)

// checkKeySpelling refuses a merged tree that decode would read under a spelling the
// operator did not choose (ADR-157). mapstructure matches a key to its field
// case-insensitively, so a root key that folds to a framework section name decodes into
// that section, while Config.Exists and the observability bootstrap look the section up by
// its exact name and read it as absent. Such a key is refused unless it is spelled as the
// section's name. It renders key names only, never a value.
func (s *configSource) checkKeySpelling() error {
	return errors.Join(misspelledSections(s.k.Raw())...)
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
