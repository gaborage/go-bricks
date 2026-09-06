package config

import (
	"github.com/knadh/koanf/maps"
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
}

// newConfigSource returns an empty source: a fresh koanf tree and a presence set in which
// every key is absent.
func newConfigSource() *configSource {
	return &configSource{k: koanf.New(koanfDelim), delivered: map[string]bool{}}
}

// loadRecording loads a provider through merge, then records as delivered every leaf key of
// the incoming source that the merge actually let through. A nil merge means koanf's own
// default merge (maps.Merge), so a layer that carried no custom merge keeps byte-identical
// semantics once it is wrapped for recording.
//
// Passing any merge func makes koanf copy the whole destination tree before merging
// (maps.Copy in koanf's Load), so recording costs one extra deep copy per operator layer at
// startup — sub-millisecond and startup-only, but not free.
func (s *configSource) loadRecording(p koanf.Provider, pa koanf.Parser, merge mergeFunc) error {
	if merge == nil {
		merge = func(src, dest map[string]any) error {
			maps.Merge(src, dest)
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

// koanfTree yields the loaded koanf instance, or nil when the Config has no source at all —
// a hand-built literal. Value access guards on that; the presence doors do not need to,
// because a sourceless Config reports every key absent.
func (c *Config) koanfTree() *koanf.Koanf {
	if c == nil || c.src == nil {
		return nil
	}
	return c.src.k
}

// delivered reports whether key was delivered by one of the operator's configuration
// layers (ADR-104). It is presence only: a delivered key may still hold an empty value,
// and each door composes delivery with its own emptiness rule. It answers for LEAF keys
// only — "database.host", never "database"; a section path is never delivered.
func (c *Config) delivered(key string) bool {
	if c == nil || c.src == nil {
		return false
	}
	return c.src.delivered[key]
}
