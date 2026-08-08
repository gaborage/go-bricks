package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// The result cache remembers packages whose mutants on changed lines were ALL
// judged clean, so amending a one-line fix on a 22-package branch re-mutates the
// package that moved instead of all 22. Every rule below exists because this is a
// gate: a false pass costs far more than a slow run, so the cache is built to be
// wrong only in the direction of doing work twice.
//
//  1. PASS only, and only a *fully* clean pass. A survivor is never stored, and
//     neither is a NOT COVERED or TIMED OUT mutant or a vacuous package. A
//     timeout is indeterminate and load-dependent (timeout.go), so caching one
//     would let a laptop under thermal load mint a permanent pass for a mutant
//     that was never evaluated. Refusing every non-clean verdict also means a
//     hit contributes nothing to the report, so the final verdict lines read
//     exactly as they would have without the cache.
//  2. The key covers everything that can change a verdict: the package, the
//     content of every .go file (test files included — the package's own tests
//     are what kill its mutants) in the package subtree the engine mutates AND
//     in its transitive first-party dependency closure, testdata/ under those
//     dirs, the pinned engine command (which carries its version), the engine
//     config that selects the mutator set, the module pins, and the toolchain
//     and platform the verdict was produced on.
//  3. The judged line set is compared, not hashed. A cached pass proves only
//     that mutants on the lines judged *at cache time* died, so a run that would
//     judge a line the cached run did not is a MISS. See rangesCovered.
//  4. Every error, absence, or ambiguity is a miss. Nothing in this file can
//     turn a package that should run into one that does not.
//  5. Entries carry cacheSchema and are ignored when it differs, so a future
//     format change cannot be misread as a pass.
const (
	// cacheSchema is folded into the key AND stored in the entry. Bump it
	// whenever the entry format changes or a cached PASS starts meaning
	// something different.
	cacheSchema = 1
	// cacheDirName sits at the repo root. The repo's .gitignore is an allowlist
	// ("ignore everything, then un-ignore"), so this path is ignored without an
	// entry of its own — and must stay that way. cache_test.go pins it.
	cacheDirName = ".mutatediff-cache"
	// cacheTTL bounds how long an entry is trusted and how large the directory
	// grows. The key already covers every input that decides a verdict, so this
	// is hygiene rather than a correctness lever.
	cacheTTL = 30 * 24 * time.Hour
)

// cacheEntry is the on-disk record of one package's clean verdict. Key is stored
// inside the file that is named after it, so a truncated write, a stale filename,
// or a hand-edited file reads as a miss instead of as a pass for some other
// input set.
type cacheEntry struct {
	Schema  int                    `json:"schema"`
	Key     string                 `json:"key"`
	Package string                 `json:"package"`
	Ranges  map[string][]lineRange `json:"ranges"`
	Stored  time.Time              `json:"stored"`
}

// resultCache is nil when caching is off or could not be initialized. Every
// method tolerates a nil receiver, so the caller never branches on it: a
// disabled cache simply never hits and never stores.
type resultCache struct {
	root   string
	dir    string
	static string
	graph  *packageGraph
	// keys memoizes each package's key from the lookup, so store can detect a
	// working tree that moved while the engine was running.
	keys map[string]string
}

// newResultCache builds the fingerprinting machinery once per run. Any failure
// returns nil — a run without a cache, never a run that skips on a guess.
func newResultCache(ctx context.Context, engine, callerGoflags string, out io.Writer) *resultCache {
	root, err := repoRoot()
	if err != nil {
		fmt.Fprintf(out, "WARN: result cache disabled (%v)\n", err)
		return nil
	}
	graph, err := loadPackageGraph(ctx, root)
	if err != nil {
		fmt.Fprintf(out, "WARN: result cache disabled (%v)\n", err)
		return nil
	}
	static, err := staticFingerprint(root, engine, callerGoflags)
	if err != nil {
		fmt.Fprintf(out, "WARN: result cache disabled (%v)\n", err)
		return nil
	}
	dir := filepath.Join(root, cacheDirName)
	pruneCache(dir)
	return &resultCache{root: root, dir: dir, static: static, graph: graph, keys: map[string]string{}}
}

// repoRoot resolves symlinks because `go list` reports resolved directories:
// on macOS a /var/folders working directory would otherwise never be a prefix of
// the /private/var/folders paths it prints, and every package would look external.
func repoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("working directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(wd)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	return resolved, nil
}

// hit reports whether pkg's mutants on the lines this run would judge have
// already been judged clean under identical inputs.
func (c *resultCache) hit(pkg string, changed map[string][]lineRange, out io.Writer) bool {
	if c == nil {
		return false
	}
	key, err := c.keyFor(pkg)
	if err != nil {
		fmt.Fprintf(out, "WARN: could not fingerprint %s for the result cache (%v) — running it\n", pkg, err)
		return false
	}
	c.keys[pkg] = key
	entry, err := readCacheEntry(filepath.Join(c.dir, key+".json"))
	if err != nil {
		return false
	}
	if entry.Schema != cacheSchema || entry.Key != key || entry.Package != pkg {
		return false
	}
	// A negative age is a clock that moved, which is exactly the ambiguity rule
	// 4 says to resolve by running the package.
	if age := time.Since(entry.Stored); age < 0 || age > cacheTTL {
		return false
	}
	return rangesCovered(scopedRanges(pkg, changed), entry.Ranges)
}

// store records a clean verdict. The caller decides what "clean" means; store's
// own job is to refuse a write whose inputs no longer match the ones hit looked
// up, because the engine reads the working tree and that tree can move under it.
func (c *resultCache) store(pkg string, changed map[string][]lineRange, out io.Writer) {
	if c == nil {
		return
	}
	key, err := c.keyFor(pkg)
	if err != nil {
		return
	}
	if prev, ok := c.keys[pkg]; !ok || prev != key {
		fmt.Fprintf(out, "WARN: %s changed while it was being mutated — not caching the result\n", pkg)
		return
	}
	entry := cacheEntry{
		Schema:  cacheSchema,
		Key:     key,
		Package: pkg,
		Ranges:  scopedRanges(pkg, changed),
		Stored:  time.Now().UTC(),
	}
	if writeErr := writeCacheEntry(c.dir, key, entry); writeErr != nil {
		fmt.Fprintf(out, "WARN: could not cache the result for %s: %v\n", pkg, writeErr)
	}
}

// keyFor hashes every input that can change pkg's verdict. It deliberately does
// NOT include the worker count, the CPU budget, or the timeout ceiling: those
// move timings only, and a run with any timing-sensitive verdict (TIMED OUT, or
// a vacuous package) is never stored in the first place.
func (c *resultCache) keyFor(pkg string) (string, error) {
	dirs, err := c.graph.closureDirs(pkgDirOf(pkg))
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "mutatediff\x00schema=%d\x00pkg=%s\x00static=%s\n", cacheSchema, pkg, c.static)
	for _, d := range dirs {
		fmt.Fprintf(h, "dir=%s\n", d)
		if dirErr := hashPackageDir(h, c.root, d); dirErr != nil {
			return "", dirErr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// staticFingerprint folds in what lives outside the package graph: the pinned
// engine command (its @version is part of the string), the toolchain and target
// platform that compiled the mutants, the caller's GOFLAGS — which can carry
// -tags and so decide which files exist at all — and the repo-level files that
// select the mutator set and pin every dependency version.
func staticFingerprint(root, engine, callerGoflags string) (string, error) {
	h := sha256.New()
	fmt.Fprintf(h, "schema=%d\n", cacheSchema)
	fmt.Fprintf(h, "engine=%s\n", engine)
	fmt.Fprintf(h, "toolchain=%s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(h, "goflags=%s\n", callerGoflags)
	for _, name := range []string{"go.mod", "go.sum", "go.work", "go.work.sum", ".gremlins.yaml"} {
		if err := hashOptionalFile(h, filepath.Join(root, name), name); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// scopedRanges narrows the repo-wide changed-line map to the files this
// package's engine run can judge. It mirrors changedMutants' own selector: that
// function resolves a report entry to pkgDir + file_name, so the reachable set
// is exactly the files under pkgDir — the whole repo when pkgDir is ".".
func scopedRanges(pkg string, changed map[string][]lineRange) map[string][]lineRange {
	dir := pkgDirOf(pkg)
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
	}
	scoped := map[string][]lineRange{}
	for file, ranges := range changed {
		if strings.HasPrefix(file, prefix) {
			scoped[file] = ranges
		}
	}
	return scoped
}

// rangesCovered is rule 3. A cached pass is evidence about the lines it judged
// and nothing else, so every line this run would judge must already be inside
// the cached run's ranges. An exact match or a narrower set hits; one extra line
// — a widened hunk, a file the cached run never saw — misses.
//
// The line-by-line walk is deliberate over interval arithmetic: hunks are small,
// and "is every line I am about to judge inside a line that was judged" is the
// property, stated as the property.
func rangesCovered(now, cached map[string][]lineRange) bool {
	for file, ranges := range now {
		judged, ok := cached[file]
		if !ok {
			return false
		}
		for _, r := range ranges {
			for line := r.Start; line < r.End; line++ {
				if !inRanges(line, judged) {
					return false
				}
			}
		}
	}
	return true
}

// pkgDirOf converts the engine's package argument ("." or "./config") into the
// repo-relative directory the rest of this file keys off.
func pkgDirOf(pkg string) string {
	dir := strings.TrimSuffix(strings.TrimPrefix(pkg, "./"), "/")
	if dir == "" {
		return "."
	}
	return dir
}

// ---- package graph ----

// listedPackage is the slice of `go list` output the cache reads.
type listedPackage struct {
	ImportPath string   `json:"ImportPath"`
	Dir        string   `json:"Dir"`
	Deps       []string `json:"Deps"`
}

// packageGraph indexes one `go list -deps -test ./...` so every package's
// first-party dependency closure resolves without shelling out again per package.
type packageGraph struct {
	// dirOf maps an import path to its repo-relative directory, or "" for a
	// package outside the repo. Presence is what matters: an import path absent
	// from this map is an unresolvable dependency, which fails the key.
	dirOf map[string]string
	// depsOf is `go list`'s transitive dependency list, already flattened.
	depsOf map[string][]string
	// inDir groups import paths by directory; one directory holds the package
	// and the synthetic test packages `go list -test` emits for it.
	inDir map[string][]string
	// dirs is inDir's key set, sorted, for subtree prefix scans.
	dirs []string
}

// loadPackageGraph lists the module with test dependencies included. -test is
// what makes a test-only import (a fake, a fixture package) part of the closure:
// it changes what the package's own tests do, and the package's own tests are
// what decide whether a mutant lives.
//
// Load failures cannot hide a change, which is why -e is not needed. A package
// `go list` cannot parse keeps its directory in the listing but loses its Deps,
// so it drops out of its dependents' closures — that changes their keys and
// forces a miss instead of masking the edit. A dependency that vanishes from the
// listing altogether is rejected outright by closureDirs. The command itself
// fails only when the module cannot be resolved at all, and that disables the
// cache for the whole run.
func loadPackageGraph(ctx context.Context, root string) (*packageGraph, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "-test", "-json=ImportPath,Dir,Deps", "./...")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list for the result cache: %w", err)
	}
	g := &packageGraph{
		dirOf:  map[string]string{},
		depsOf: map[string][]string{},
		inDir:  map[string][]string{},
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	for {
		var p listedPackage
		if decErr := dec.Decode(&p); errors.Is(decErr, io.EOF) {
			break
		} else if decErr != nil {
			return nil, fmt.Errorf("parse go list output: %w", decErr)
		}
		if p.Dir == "" {
			// Unresolvable: leave it out of dirOf so any dependent fails its key.
			continue
		}
		rel := relativeDir(root, p.Dir)
		g.dirOf[p.ImportPath] = rel
		g.depsOf[p.ImportPath] = p.Deps
		if rel != "" {
			g.inDir[rel] = append(g.inDir[rel], p.ImportPath)
		}
	}
	for d := range g.inDir {
		g.dirs = append(g.dirs, d)
	}
	sort.Strings(g.dirs)
	if len(g.dirs) == 0 {
		return nil, errors.New("go list produced no first-party packages")
	}
	return g, nil
}

// relativeDir returns dir relative to root in slash form, or "" when dir is
// outside the repo (the standard library, the module cache).
func relativeDir(root, dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return ""
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return ""
	}
	return rel
}

// closureDirs returns every repo-relative directory whose contents can change
// pkgDir's verdict: the whole subtree the engine mutates (gremlins mutates
// recursively from the directory it is pointed at), plus the first-party
// transitive dependency closure of every package in that subtree. The closure
// matters because a mutant in X is killed by X's tests, but X's *behavior*
// changes when a package X imports changes.
func (g *packageGraph) closureDirs(pkgDir string) ([]string, error) {
	targets := g.subtree(pkgDir)
	if len(targets) == 0 {
		return nil, fmt.Errorf("no Go package listed under %q", pkgDir)
	}
	set := map[string]bool{}
	for _, importPath := range targets {
		set[g.dirOf[importPath]] = true
		for _, dep := range g.depsOf[importPath] {
			dir, ok := g.dirOf[dep]
			if !ok {
				return nil, fmt.Errorf("dependency %q of %q is missing from the package listing", dep, importPath)
			}
			if dir != "" {
				set[dir] = true
			}
		}
	}
	delete(set, "")
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs, nil
}

// subtree lists every import path declared at or below pkgDir. "." is the whole
// repo, which is what pointing the engine at the root directory would mutate.
func (g *packageGraph) subtree(pkgDir string) []string {
	var out []string
	for _, d := range g.dirs {
		if pkgDir == "." || d == pkgDir || strings.HasPrefix(d, pkgDir+"/") {
			out = append(out, g.inDir[d]...)
		}
	}
	return out
}

// ---- hashing ----

// hashPackageDir folds one directory's verdict-deciding inputs into h: every .go
// file in it, and everything under its testdata/. Test files are included
// because they are what kills mutants; testdata is included because a golden
// file decides an outcome exactly as source does. Build-excluded .go files are
// hashed too — over-invalidation costs a re-run, under-invalidation costs the gate.
func hashPackageDir(h io.Writer, root, relDir string) error {
	dir := filepath.Join(root, filepath.FromSlash(relDir))
	entries, err := os.ReadDir(dir) // sorted by name, so the digest is order-stable
	if err != nil {
		return fmt.Errorf("read %s: %w", relDir, err)
	}
	for _, e := range entries {
		switch {
		case e.IsDir():
			if e.Name() != "testdata" {
				continue
			}
			if walkErr := hashTree(h, filepath.Join(dir, e.Name()), path.Join(relDir, e.Name())); walkErr != nil {
				return walkErr
			}
		case strings.HasSuffix(e.Name(), ".go"):
			if fileErr := hashFile(h, filepath.Join(dir, e.Name()), path.Join(relDir, e.Name())); fileErr != nil {
				return fileErr
			}
		}
	}
	return nil
}

// hashTree folds a whole directory tree in. WalkDir walks lexically, so the
// digest does not depend on filesystem ordering.
func hashTree(h io.Writer, absRoot, label string) error {
	return filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, p)
		if relErr != nil {
			return relErr
		}
		return hashFile(h, p, path.Join(label, filepath.ToSlash(rel)))
	})
}

// hashOptionalFile folds a repo-level file in, recording its absence explicitly
// so "no go.work" and "an empty go.work" are different keys.
func hashOptionalFile(h io.Writer, abs, label string) error {
	err := hashFile(h, abs, label)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(h, "%s absent\n", label)
		return nil
	}
	return err
}

// hashFile writes "<label> <sha256>" into h. Digesting per file rather than
// streaming raw bytes keeps the path and the content unambiguously separated,
// so renaming a file cannot produce the same digest as editing it.
func hashFile(h io.Writer, abs, label string) error {
	f, err := os.Open(abs) // #nosec G304 -- paths come from `go list` output under the repo root, not user input
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	fileHash := sha256.New()
	if _, copyErr := io.Copy(fileHash, f); copyErr != nil {
		return copyErr
	}
	fmt.Fprintf(h, "%s %x\n", label, fileHash.Sum(nil))
	return nil
}

// ---- storage ----

func readCacheEntry(p string) (cacheEntry, error) {
	raw, err := os.ReadFile(p) // #nosec G304 -- <cache dir>/<hex key>.json, both derived in-process
	if err != nil {
		return cacheEntry{}, err
	}
	var e cacheEntry
	if unmarshalErr := json.Unmarshal(raw, &e); unmarshalErr != nil {
		return cacheEntry{}, unmarshalErr
	}
	return e, nil
}

// writeCacheEntry writes through a temp file and renames, so an interrupted run
// leaves either the previous entry or none — never a half-written one that a
// later run would have to parse.
func writeCacheEntry(dir, key string, e cacheEntry) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "entry-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, writeErr := tmp.Write(raw); writeErr != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return writeErr
	}
	if closeErr := tmp.Close(); closeErr != nil {
		_ = os.Remove(name)
		return closeErr
	}
	return os.Rename(name, filepath.Join(dir, key+".json"))
}

// pruneCache drops entries past the TTL. Best effort by design: a failure here
// costs disk, never correctness.
func pruneCache(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-cacheTTL)
	for _, e := range entries {
		info, infoErr := e.Info()
		if infoErr != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}
