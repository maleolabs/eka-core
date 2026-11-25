package codegraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// cacheTree is a small tree with a Go file so a build must read and parse.
func cacheTree(t *testing.T) (root, cache string) {
	t.Helper()
	root = t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	writeFile(t, filepath.Join(root, "pkg", "code.go"), "package pkg\nfunc Code(){}\n")
	writeFile(t, filepath.Join(root, "notes.xyz"), "plain")
	return root, filepath.Join(t.TempDir(), "index.json")
}

// touch sets an explicit mtime so size-only or mtime-only changes are exact.
func touch(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if e := os.Chtimes(path, mtime, mtime); e != nil {
		t.Fatal(e)
	}
}

// A cache hit must decide freshness from metadata alone: no os.ReadFile, no
// sha256 over content, no parseGo. parseCount is the observable build counter.
func TestCacheHitParsesNothing(t *testing.T) {
	root, cache := cacheTree(t)

	first, hit, e := LoadOrBuild(root, cache)
	if e != nil || hit {
		t.Fatalf("first LoadOrBuild: hit=%v err=%v", hit, e)
	}
	built := parseCount
	if built == 0 {
		t.Fatalf("first call did not build (parseCount=%d)", built)
	}
	if first.StatKey == "" {
		t.Fatal("built index has empty StatKey")
	}

	second, hit, e := LoadOrBuild(root, cache)
	if e != nil || !hit {
		t.Fatalf("second LoadOrBuild: hit=%v err=%v", hit, e)
	}
	if parseCount != built {
		t.Fatalf("cache hit parsed: parseCount %d -> %d", built, parseCount)
	}
	if second.Digest != first.Digest || len(second.Files) != len(first.Files) {
		t.Fatalf("hit returned different index: %+v vs %+v", second, first)
	}
}

// Same size + different mtime must miss, changed size must miss. This proves
// the fingerprint is genuinely stat-derived rather than content- or name-only.
func TestCacheStatKeyInvalidation(t *testing.T) {
	root, cache := cacheTree(t)
	f := filepath.Join(root, "notes.xyz")
	base := time.Unix(1700000000, 0)
	touch(t, f, base)

	first, hit, e := LoadOrBuild(root, cache)
	if e != nil || hit {
		t.Fatalf("first LoadOrBuild: hit=%v err=%v", hit, e)
	}
	before := parseCount
	if _, hit, e := LoadOrBuild(root, cache); e != nil || !hit {
		t.Fatalf("unchanged tree hit=%v err=%v, want hit", hit, e)
	}
	if parseCount != before {
		t.Fatalf("hit parsed: %d -> %d", before, parseCount)
	}

	// Same size, different mtime.
	touch(t, f, base.Add(time.Second))
	if _, hit, e := LoadOrBuild(root, cache); e != nil || hit {
		t.Fatalf("mtime change hit=%v err=%v, want miss", hit, e)
	}

	// Different size.
	writeFile(t, f, "plainly")
	third, hit, e := LoadOrBuild(root, cache)
	if e != nil || hit {
		t.Fatalf("size change hit=%v err=%v, want miss", hit, e)
	}
	if third.Digest == first.Digest {
		t.Fatal("size change produced the same digest")
	}
}

// A cache written before StatKey existed must not be trusted as a hit, but the
// returned index must still be the full, correct build.
func TestCacheOldSchemaWithoutStatKeyFallsBack(t *testing.T) {
	root, cache := cacheTree(t)
	fresh, e := Build(root)
	if e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(fresh)
	if e != nil {
		t.Fatal(e)
	}
	var legacy map[string]any
	if e = json.Unmarshal(raw, &legacy); e != nil {
		t.Fatal(e)
	}
	delete(legacy, "statKey")
	raw, e = json.Marshal(legacy)
	if e != nil {
		t.Fatal(e)
	}
	writeFile(t, cache, string(raw))

	before := parseCount
	got, hit, e := LoadOrBuild(root, cache)
	if e != nil {
		t.Fatal(e)
	}
	if hit {
		t.Fatal("legacy cache without statKey returned a hit")
	}
	if parseCount == before {
		t.Fatal("legacy cache did not rebuild")
	}
	if got.Digest != fresh.Digest || len(got.Files) != len(fresh.Files) || got.StatKey == "" {
		t.Fatalf("legacy fallback index=%+v want digest %q", got, fresh.Digest)
	}
	// The rewrite must upgrade the cache so the next call is a hit.
	if _, hit, e := LoadOrBuild(root, cache); e != nil || !hit {
		t.Fatalf("upgraded cache hit=%v err=%v, want hit", hit, e)
	}
}

// Files inside skipped dirs must not perturb the fingerprint, otherwise a
// build artifact (.git index, node_modules churn) would invalidate the cache.
func TestStatKeyIgnoresSkippedDirs(t *testing.T) {
	root, cache := cacheTree(t)
	if _, _, e := LoadOrBuild(root, cache); e != nil {
		t.Fatal(e)
	}
	base, e := Load(cache)
	if e != nil {
		t.Fatal(e)
	}
	writeFile(t, filepath.Join(root, ".git", "index"), "churn")
	writeFile(t, filepath.Join(root, "node_modules", "m.js"), "churn")
	writeFile(t, filepath.Join(root, "exchange", "snap.json"), "{}")

	if _, hit, e := LoadOrBuild(root, cache); e != nil || !hit {
		t.Fatalf("skipped-dir churn hit=%v err=%v, want hit", hit, e)
	}
	now, e := currentStatKey(root, "", DefaultBuildOptions())
	if e != nil {
		t.Fatal(e)
	}
	if now != base.StatKey {
		t.Fatalf("statKey changed after skipped-dir churn: %q vs %q", now, base.StatKey)
	}
}

// With FollowSymlinks=false a symlinked dir is never inventoried, so it must
// not appear in the fingerprint either.
func TestStatKeyNoFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	real := filepath.Join(root, "real")
	writeFile(t, filepath.Join(real, "plug.go"), "package real\nfunc Plug(){}\n")
	if e := os.Symlink(real, filepath.Join(root, "link")); e != nil {
		t.Skipf("symlinks unsupported: %v", e)
	}
	opts := BuildOptions{FollowSymlinks: false}
	before, e := currentStatKey(root, "", opts)
	if e != nil {
		t.Fatal(e)
	}
	idx, e := BuildWithOptions(root, opts)
	if e != nil {
		t.Fatal(e)
	}
	// Only main.go and real/plug.go: link/plug.go is not enumerated at all, so
	// the fingerprint is over exactly the inventoried set.
	if len(idx.Files) != 2 || indexPaths(idx)["link/plug.go"] || idx.StatKey != before {
		t.Fatalf("files=%+v statKey %q vs %q", indexPaths(idx), idx.StatKey, before)
	}
	// Touching the real file must move the fingerprint (the symlink skip must
	// not have become skip-everything).
	touch(t, filepath.Join(real, "plug.go"), time.Unix(1700000000, 0))
	after, e := currentStatKey(root, "", opts)
	if e != nil {
		t.Fatal(e)
	}
	if after == before {
		t.Fatal("touching an indexed file did not change the fingerprint")
	}
	idx2, e := BuildWithOptions(root, opts)
	if e != nil {
		t.Fatal(e)
	}
	if idx2.StatKey != after {
		t.Fatalf("build statKey %q != stat key %q", idx2.StatKey, after)
	}
	if len(idx2.Files) != 2 || indexPaths(idx2)["link/plug.go"] {
		t.Fatalf("symlinked dir inventoried with follow disabled: %+v", indexPaths(idx2))
	}
}
