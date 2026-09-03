// Package codegraph provides bounded, deterministic source inventory and context queries.
package codegraph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	MaxSymbols = 32
	MaxUnits   = 64
)

type File struct {
	Path     string   `json:"path"`
	Language string   `json:"language"`
	Digest   string   `json:"digest"`
	Size     int64    `json:"size"`
	Symbols  []Symbol `json:"symbols,omitempty"`
	Imports  []string `json:"imports,omitempty"`
}
type Symbol struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Exported bool   `json:"exported"`
}
type Ref struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}
type Index struct {
	Root    string   `json:"root"`
	Digest  string   `json:"digest"`
	Files   []File   `json:"files"`
	Symbols []Symbol `json:"symbols"`
	Refs    []Ref    `json:"refs"`
}

// Build scans root. Unsupported files remain inventory entries without parsed symbols.
func Build(root string) (Index, error) {
	return build(root, "")
}
func build(root, skip string) (Index, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Index{}, err
	}
	idx := Index{Root: abs}
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != abs && (d.Name() == ".git" || d.Name() == "vendor" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, e := filepath.Rel(abs, path)
		if e != nil {
			return e
		}
		if strings.HasPrefix(rel, ".git/") {
			return nil
		}
		if skip != "" {
			if same, _ := filepath.Abs(path); same == skip {
				return nil
			}
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		sum := sha256.Sum256(b)
		f := File{Path: filepath.ToSlash(rel), Language: language(path), Digest: hex.EncodeToString(sum[:]), Size: int64(len(b))}
		if f.Language == "go" {
			parseGo(path, f.Path, b, &f, &idx)
		}
		idx.Files = append(idx.Files, f)
		return nil
	})
	if err != nil {
		return Index{}, err
	}
	sort.Slice(idx.Files, func(i, j int) bool { return idx.Files[i].Path < idx.Files[j].Path })
	sort.Slice(idx.Symbols, func(i, j int) bool { return symbolKey(idx.Symbols[i]) < symbolKey(idx.Symbols[j]) })
	sort.Slice(idx.Refs, func(i, j int) bool { return refKey(idx.Refs[i]) < refKey(idx.Refs[j]) })
	idx.Digest = digest(idx.Files)
	return idx, nil
}

func language(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".go") {
		return "go"
	}
	return "unsupported"
}
func parseGo(path, rel string, b []byte, f *File, idx *Index) {
	fset := token.NewFileSet()
	p, err := parser.ParseFile(fset, path, b, 0)
	if err != nil {
		return
	}
	for _, imp := range p.Imports {
		f.Imports = append(f.Imports, strings.Trim(imp.Path.Value, `"`))
	}
	ast.Inspect(p, func(n ast.Node) bool {
		var name, kind string
		switch x := n.(type) {
		case *ast.FuncDecl:
			name = x.Name.Name
			kind = "function"
		case *ast.TypeSpec:
			name = x.Name.Name
			kind = "type"
		case *ast.ValueSpec:
			kind = "value"
			for _, id := range x.Names {
				addSymbol(id.Name, kind, rel, id.Pos(), fset, idx)
			}
			return true
		}
		if name != "" {
			addSymbol(name, kind, rel, n.Pos(), fset, idx)
		}
		return true
	})
	for _, imp := range f.Imports {
		idx.Refs = append(idx.Refs, Ref{Path: rel, Kind: "import", Name: imp})
	}
}
func addSymbol(name, kind, rel string, pos token.Pos, fset *token.FileSet, idx *Index) {
	line := fset.Position(pos).Line
	idx.Symbols = append(idx.Symbols, Symbol{Name: name, Kind: kind, File: rel, Line: line, Exported: ast.IsExported(name)})
}
func symbolKey(s Symbol) string {
	return s.File + "\x00" + fmt.Sprintf("%08d", s.Line) + "\x00" + s.Name + "\x00" + s.Kind
}
func refKey(r Ref) string { return r.Path + "\x00" + r.Kind + "\x00" + r.Name }
func digest(files []File) string {
	h := sha256.New()
	for _, f := range files {
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%d\n", f.Path, f.Language, f.Digest, f.Size)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Save writes stable JSON cache. Cache is derived data and can be deleted safely.
func (i Index) Save(path string) error {
	b, e := json.Marshal(i)
	if e != nil {
		return e
	}
	tmp := path + ".tmp"
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return e
	}
	return os.Rename(tmp, path)
}
func Load(path string) (Index, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return Index{}, e
	}
	var i Index
	if e = json.Unmarshal(b, &i); e != nil {
		return Index{}, e
	}
	return i, nil
}

// LoadOrBuild returns cached index only when root and current inventory digest match.
func LoadOrBuild(root, cachePath string) (Index, bool, error) {
	skip, _ := filepath.Abs(cachePath)
	i, e := Load(cachePath)
	if e == nil {
		fresh, e2 := build(root, skip)
		if e2 != nil {
			return Index{}, false, e2
		}
		if i.Root == fresh.Root && i.Digest == fresh.Digest {
			return i, true, nil
		}
	}
	fresh, e := build(root, skip)
	if e != nil {
		return Index{}, false, e
	}
	if cachePath != "" {
		if e = fresh.Save(cachePath); e != nil {
			return Index{}, false, e
		}
	}
	return fresh, false, nil
}

type Depth string

const (
	DepthLocal       Depth = "local"
	DepthDependency  Depth = "dependency"
	DepthEngineering Depth = "engineering"
)

type Request struct {
	Focus     string `json:"focus"`
	Depth     Depth  `json:"depth"`
	Level     int    `json:"level"`
	NoContent bool   `json:"noContent"`
}
type Response struct {
	Schema      string     `json:"schema"`
	Query       Request    `json:"query"`
	IndexDigest string     `json:"indexDigest"`
	Provenance  Provenance `json:"provenance"`
	Units       []Unit     `json:"units"`
	Symbols     []Symbol   `json:"symbols"`
	Refs        []Ref      `json:"refs,omitempty"`
}
type Unit struct {
	Path     string `json:"path"`
	Language string `json:"language"`
	Digest   string `json:"digest"`
	Size     int64  `json:"size"`
	Content  string `json:"content,omitempty"`
}
type Provenance struct {
	Source     string  `json:"source"`
	Method     string  `json:"method"`
	Confidence float64 `json:"confidence"`
}

// Serve selects bounded context. Level 0 inventory, 1 symbols, 2 imports, 3 source snippets.
func Serve(i Index, q Request) (Response, error) {
	if q.Level < 0 || q.Level > 3 {
		return Response{}, fmt.Errorf("level must be 0..3")
	}
	if q.Depth == "" {
		q.Depth = DepthDependency
	}
	if q.Depth != DepthLocal && q.Depth != DepthDependency && q.Depth != DepthEngineering {
		return Response{}, fmt.Errorf("unsupported depth %q", q.Depth)
	}
	r := Response{Schema: "eka/code-context/1", Query: q, IndexDigest: i.Digest, Provenance: Provenance{Source: "local-index", Method: "deterministic-inventory-and-go-ast", Confidence: 1}}
	matched := map[string]bool{}
	for _, f := range i.Files {
		if q.Focus == "" || strings.Contains(strings.ToLower(f.Path), strings.ToLower(q.Focus)) {
			matched[f.Path] = true
			r.Units = append(r.Units, Unit{Path: f.Path, Language: f.Language, Digest: f.Digest, Size: f.Size})
		}
	}
	if q.Focus != "" {
		for _, s := range i.Symbols {
			if strings.EqualFold(s.Name, q.Focus) {
				matched[s.File] = true
			}
		}
		if len(r.Units) == 0 {
			for _, f := range i.Files {
				if matched[f.Path] {
					r.Units = append(r.Units, Unit{Path: f.Path, Language: f.Language, Digest: f.Digest, Size: f.Size})
				}
			}
		}
	}
	if len(r.Units) > MaxUnits {
		r.Units = r.Units[:MaxUnits]
	}
	if q.Depth == DepthEngineering && q.Focus != "" && len(r.Units) == 0 {
		for _, f := range i.Files {
			r.Units = append(r.Units, Unit{Path: f.Path, Language: f.Language, Digest: f.Digest, Size: f.Size})
			if len(r.Units) == MaxUnits {
				break
			}
		}
	}
	if q.Level >= 1 {
		for _, s := range i.Symbols {
			if len(r.Symbols) >= MaxSymbols {
				break
			}
			if q.Focus == "" || strings.Contains(strings.ToLower(s.File), strings.ToLower(q.Focus)) || strings.Contains(strings.ToLower(s.Name), strings.ToLower(q.Focus)) {
				r.Symbols = append(r.Symbols, s)
			}
		}
	}
	if q.Level >= 2 {
		for _, ref := range i.Refs {
			if len(r.Refs) >= MaxUnits {
				break
			}
			if q.Focus == "" || matched[ref.Path] || strings.Contains(strings.ToLower(ref.Path), strings.ToLower(q.Focus)) {
				r.Refs = append(r.Refs, ref)
			}
		}
	}
	if q.Level == 3 && !q.NoContent {
		for n := range r.Units {
			b, e := os.ReadFile(filepath.Join(i.Root, filepath.FromSlash(r.Units[n].Path)))
			if e == nil {
				r.Units[n].Content = string(b)
			}
		}
	}
	return r, nil
}
