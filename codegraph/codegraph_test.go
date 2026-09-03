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
