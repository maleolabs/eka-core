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
