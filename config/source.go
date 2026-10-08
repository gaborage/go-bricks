package config

import (
	"strings"

	koanfmaps "github.com/knadh/koanf/maps"
	"github.com/knadh/koanf/v2"
)

// koanfDelim is the key delimiter every koanf tree in this package is built with; the
// presence recorder joins path segments with the same one.
const koanfDelim = "."

// configSource is a loaded koanf tree paired with the presence set recorded while it was
// built. PRESENCE is whether a key was delivered by one of the operator's layers — the base
// YAML, the environment overlay, or an environment variable — as opposed to reaching koanf
// from the framework's preloaded defaults (ADR-104). The two travel together as one value
// so a Config can never hold a tree without the record of what was actually delivered.
type configSource struct {
	k         *koanf.Koanf
	delivered map[string]bool
	// sequences holds the dotted path of every YAML sequence an operator layer wrote where
	// the keystore.keys or messaging.seal.active walk expects a map, recorded before the
	// merge. A sequence that survives is refused at decode; one a later layer replaced never
	// reaches decode, so checkLayerSequences refuses it instead (ADR-144).
	sequences map[string]bool
}

// newConfigSource returns an empty source: a fresh koanf tree and a presence set in which
// every key is absent.
func newConfigSource() *configSource {
	return &configSource{
		k:         koanf.New(koanfDelim),
		delivered: map[string]bool{},
		sequences: map[string]bool{},
	}
}

// loadRecording loads a provider through merge, then records as delivered every leaf key of
// the incoming source that the merge actually let through. A nil merge means koanf's own
// default merge (maps.Merge), so a layer that carried no custom merge keeps byte-identical
// semantics once it is wrapped for recording.
//
// koanf deep-copies the whole destination tree (maps.Copy in its merge) whenever a merge
// func is set, so the two YAML layers newly pay that copy while the environment layer, which
// already carried one, does not — sub-millisecond and startup-only, but not free.
func (s *configSource) loadRecording(p koanf.Provider, pa koanf.Parser, merge mergeFunc) error {
	if merge == nil {
		merge = func(src, dest map[string]any) error {
			koanfmaps.Merge(src, dest)
			return nil
		}
	}
	return s.k.Load(p, pa, koanf.WithMergeFunc(s.recording(merge)))
}

// mergeFunc is the koanf.WithMergeFunc shape: merge src into dest, left to right.
type mergeFunc func(src, dest map[string]any) error

// recording wraps merge so presence is recorded AFTER the merge has decided. A scalar the
// merge dropped — skipScalarOverMapMerge refusing to clobber a map node — never reached the
// tree and so is never recorded as delivered.
func (s *configSource) recording(merge mergeFunc) mergeFunc {
	return func(src, dest map[string]any) error {
		s.recordSequences(src)
		if err := merge(src, dest); err != nil {
			return err
		}
		s.record(src, dest, "")
		return nil
	}
}

// record walks src alongside the already-merged dest, recording the dotted path of every
// src leaf that survived. dest holds a map at a path exactly where the merge kept the
// structured config and dropped an incoming scalar, so that path is skipped; a nil scalar
// (YAML null) did reach the tree and is recorded.
func (s *configSource) record(src, dest map[string]any, prefix string) {
	for key, srcVal := range src {
		path := key
		if prefix != "" {
			path = prefix + koanfDelim + key
		}

		destMap, destIsMap := dest[key].(map[string]any)
		if srcMap, srcIsMap := srcVal.(map[string]any); srcIsMap {
			// A merged map node is a map in dest either way (recursed into, or set
			// wholesale), so its leaves are reached by descending into it.
			if destIsMap {
				s.record(srcMap, destMap, path)
			}
			continue
		}
		if destIsMap {
			continue
		}
		s.delivered[path] = true
	}
}

// recordSequences records where an incoming layer holds a sequence that the decode walk
// would refuse: at keystore.keys or at any name below it, down to the first entry (inside an
// entry the field schema governs, and a later layer that sets a source there drops no entry),
// and at messaging.seal.active or anywhere below it.
func (s *configSource) recordSequences(src map[string]any) {
	eachSubtree(src, fieldKeystoreKeys, func(node any) { s.recordSequencesBelow(node, fieldKeystoreKeys, true) })
	eachSubtree(src, fieldMessagingSealActive, func(node any) { s.recordSequencesBelow(node, fieldMessagingSealActive, false) })
}

// recordSequencesBelow records node at path when it is a sequence, and otherwise descends
// into a map's children; skipEntries leaves out a child the keystore walk reads as an entry.
func (s *configSource) recordSequencesBelow(node any, path string, skipEntries bool) {
	if isSequence(node) {
		s.sequences[path] = true
		return
	}
	tree, isMap := stringMap(node)
	if !isMap {
		return
	}
	for key, child := range tree {
		if skipEntries && isKeyEntry(child) {
			continue
		}
		s.recordSequencesBelow(child, path+koanfDelim+key, skipEntries)
	}
}

// isKeyEntry reports whether node is a map the keystore walk reads as an entry: one with a
// child that names an entry field.
func isKeyEntry(node any) bool {
	tree, isMap := stringMap(node)
	if !isMap {
		return false
	}
	_, isEntry := entryField(tree)
	return isEntry
}

// eachSubtree calls visit with every node of src at the dotted path, each segment matched as
// mapstructure matches a field name, case-insensitively, so a layer that writes Keystore: is
// read where decode reads it.
func eachSubtree(src map[string]any, path string, visit func(node any)) {
	visitSubtree(src, strings.Split(path, koanfDelim), visit)
}

func visitSubtree(node any, path []string, visit func(node any)) {
	if len(path) == 0 {
		visit(node)
		return
	}
	section, isMap := stringMap(node)
	if !isMap {
		return
	}
	for key, value := range section {
		if strings.EqualFold(key, path[0]) {
			visitSubtree(value, path[1:], visit)
		}
	}
}

// koanfTree yields the loaded koanf instance, or nil when the Config has no source at all —
// a hand-built literal. Value access guards on that; the presence doors do not need to,
// because a sourceless Config reports every key absent.
func (c *Config) koanfTree() *koanf.Koanf {
	if c == nil || c.src == nil {
		return nil
	}
	return c.src.k
}

// delivered reports whether key was recorded by one of the operator's configuration layers
// AND is still present in the final merged tree (ADR-104). Recording is append-only, so the
// second half is what evicts a key a later layer replaced or nulled at an ancestor path —
// exactly as koanf's own Exists did before presence was recorded. It is presence only: a
// delivered key may still hold an empty value, and each door composes delivery with its own
// emptiness rule.
//
// Doors query LEAF keys — "database.host", never "database". A YAML null written AT a
// section path is itself a leaf and records that path, so a section path must not be used as
// a door key: it would read as delivered for the very shape that empties the section.
func (c *Config) delivered(key string) bool {
	if c == nil || c.src == nil {
		return false
	}
	return c.src.delivered[key] && c.src.k.Exists(key)
}
