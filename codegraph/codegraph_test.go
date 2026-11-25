package codegraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildServe(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nimport \"fmt\"\ntype Server struct{}\nfunc Run(){}\n"), 0600)
	_ = os.WriteFile(filepath.Join(root, "notes.xyz"), []byte("plain"), 0600)
	i, e := Build(root)
	if e != nil {
		t.Fatal(e)
	}
	if len(i.Files) != 2 || i.Files[1].Language != "unsupported" {
		t.Fatalf("files=%+v", i.Files)
	}
	r, e := Serve(i, Request{Focus: "Run", Level: 3, NoContent: true})
	if e != nil || len(r.Symbols) != 1 || len(r.Units) != 1 || r.Units[0].Content != "" {
		t.Fatalf("response=%+v err=%v", r, e)
	}
	if _, e = json.Marshal(r); e != nil {
		t.Fatal(e)
	}
}
func TestCacheInvalidation(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "x.go")
	_ = os.WriteFile(f, []byte("package x"), 0600)
	c := filepath.Join(t.TempDir(), "index.json")
	i, hit, e := LoadOrBuild(root, c)
	if e != nil || hit {
		t.Fatal(e, hit)
	}
	_, hit, e = LoadOrBuild(root, c)
	if e != nil || !hit {
		t.Fatal(e, hit)
	}
	_ = os.WriteFile(f, []byte("package x\nfunc Changed(){}"), 0600)
	j, hit, e := LoadOrBuild(root, c)
	if e != nil || hit || j.Digest == i.Digest {
		t.Fatal(e, hit)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
}

func indexPaths(i Index) map[string]bool {
	m := map[string]bool{}
	for _, f := range i.Files {
		m[f.Path] = true
	}
	return m
}

// A symlinked directory (e.g. Flutter .plugin_symlinks) must be followed,
// not passed to ReadFile as if it were a regular file.
func TestBuildFollowsSymlinkedDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	real := filepath.Join(root, "real")
	writeFile(t, filepath.Join(real, "plug.go"), "package real\nfunc Plug(){}\n")
	if e := os.Symlink(real, filepath.Join(root, ".plugin_symlinks")); e != nil {
		t.Skipf("symlinks unsupported: %v", e)
	}
	i, e := Build(root)
	if e != nil {
		t.Fatalf("Build with symlinked dir: %v", e)
	}
	got := indexPaths(i)
	if !got["main.go"] || !got[".plugin_symlinks/plug.go"] {
		t.Fatalf("files=%+v", got)
	}
}

// A symlink loop (a/b -> a) must terminate via cycle detection.
func TestBuildSymlinkLoop(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	a := filepath.Join(root, "a")
	b := filepath.Join(a, "b")
	if e := os.MkdirAll(b, 0700); e != nil {
		t.Fatal(e)
	}
	writeFile(t, filepath.Join(a, "a.go"), "package a\nfunc A(){}\n")
	if e := os.Symlink(a, filepath.Join(b, "loop")); e != nil {
		t.Skipf("symlinks unsupported: %v", e)
	}
	i, e := Build(root)
	if e != nil {
		t.Fatalf("Build with symlink loop: %v", e)
	}
	got := indexPaths(i)
	if !got["main.go"] || !got["a/a.go"] {
		t.Fatalf("files=%+v", got)
	}
}

// With FollowSymlinks=false, symlinked entries are skipped.
func TestBuildNoFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	real := filepath.Join(root, "real")
	writeFile(t, filepath.Join(real, "plug.go"), "package real\nfunc Plug(){}\n")
	if e := os.Symlink(real, filepath.Join(root, "link")); e != nil {
		t.Skipf("symlinks unsupported: %v", e)
	}
	i, e := BuildWithOptions(root, BuildOptions{FollowSymlinks: false})
	if e != nil {
		t.Fatalf("BuildWithOptions no-follow: %v", e)
	}
	got := indexPaths(i)
	if !got["main.go"] || !got["real/plug.go"] {
		t.Fatalf("files=%+v", got)
	}
	if got["link/plug.go"] {
		t.Fatalf("symlinked dir indexed with follow disabled: %+v", got)
	}
}

// Transport/derived directories (exchange, .eka, drafts, feedback) are
// skipped at every level so the index reflects the pure codebase (ADR-034).
// Knowledge snapshots stay reachable via eka get/context/view, never via
// code_discover/context/get.
func TestBuildSkipsTransportDirs(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	writeFile(t, filepath.Join(root, "pkg", "code.go"), "package pkg\nfunc Code(){}\n")
	// Top-level transport dirs.
	writeFile(t, filepath.Join(root, "exchange", "snapshot.json"), `{"units":[]}`)
	writeFile(t, filepath.Join(root, ".eka", "state.md"), "state")
	writeFile(t, filepath.Join(root, "drafts", "note.md"), "draft")
	writeFile(t, filepath.Join(root, "feedback", "fb.md"), "feedback")
	// Nested transport dirs (must also be skipped).
	writeFile(t, filepath.Join(root, "pkg", "exchange", "nested.json"), `{}`)
	writeFile(t, filepath.Join(root, "sub", ".eka", "deep.md"), "deep")
	writeFile(t, filepath.Join(root, "sub", "drafts", "d.md"), "d")
	writeFile(t, filepath.Join(root, "sub", "feedback", "f.md"), "f")
	// Pre-existing skips keep working.
	writeFile(t, filepath.Join(root, "vendor", "v.go"), "package vendor\n")
	writeFile(t, filepath.Join(root, "node_modules", "m.js"), "x")
	writeFile(t, filepath.Join(root, ".git", "config"), "git")

	i, e := Build(root)
	if e != nil {
		t.Fatalf("Build: %v", e)
	}
	got := indexPaths(i)
	if !got["main.go"] || !got["pkg/code.go"] {
		t.Fatalf("pure codebase missing: %+v", got)
	}
	for p := range got {
		if p == "exchange/snapshot.json" || p == ".eka/state.md" ||
			p == "drafts/note.md" || p == "feedback/fb.md" ||
			p == "pkg/exchange/nested.json" || p == "sub/.eka/deep.md" ||
			p == "sub/drafts/d.md" || p == "sub/feedback/f.md" ||
			p == "vendor/v.go" || p == "node_modules/m.js" || p == ".git/config" {
			t.Fatalf("transport/derived path indexed: %q in %+v", p, got)
		}
	}
	// Bounds: transport noise removed, only pure files remain.
	if len(i.Files) != 2 {
		t.Fatalf("files=%d want 2 (pure codebase only): %+v", len(i.Files), got)
	}
}

// Parity: BuildWithOptions applies the same skip-list in every option path,
// and discover/get operate on the pure index only.
func TestTransportSkipParity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\nfunc Run(){}\n")
	writeFile(t, filepath.Join(root, "exchange", "snapshot.json"), `{"units":[]}`)
	writeFile(t, filepath.Join(root, ".eka", "s.md"), "s")

	for _, opts := range []BuildOptions{{FollowSymlinks: true}, {FollowSymlinks: false}} {
		i, e := BuildWithOptions(root, opts)
		if e != nil {
			t.Fatalf("BuildWithOptions(%+v): %v", opts, e)
		}
		got := indexPaths(i)
		if !got["main.go"] || len(i.Files) != 1 {
			t.Fatalf("opts %+v files=%+v", opts, got)
		}
		// Discover must not surface snapshot noise.
		d, e := Discover(i, DiscoverRequest{Query: "snapshot", Limit: 16})
		if e != nil {
			t.Fatalf("Discover: %v", e)
		}
		for _, c := range d.Candidates {
			if c.Path == "exchange/snapshot.json" {
				t.Fatalf("discover returned transport path: %+v", d.Candidates)
			}
		}
		// Get on a transport path is not-found (knowledge via eka get).
		if _, e := Get(i, GetRequest{Path: "exchange/snapshot.json"}); e == nil {
			t.Fatalf("Get(exchange/...) = found, want not-found")
		}
		// Digest parity: pure-only tree and mixed tree digest identically
		// when transport dirs are skipped.
		pure := t.TempDir()
		writeFile(t, filepath.Join(pure, "main.go"), "package main\nfunc Run(){}\n")
		pi, e := BuildWithOptions(pure, opts)
		if e != nil {
			t.Fatalf("Build pure: %v", e)
		}
		if i.Digest != pi.Digest {
			t.Fatalf("digest mismatch opts %+v: %q vs pure %q", opts, i.Digest, pi.Digest)
		}
	}
}
