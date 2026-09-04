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
	MaxSymbols    = 32
	MaxUnits      = 64
	MaxCandidates = 64
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

// DiscoverRequest is natural query plus optional scope filter.
type DiscoverRequest struct {
	Query string `json:"query"`
	Scope string `json:"scope,omitempty"`
	Limit int    `json:"limit"`
}

// Candidate is one deterministic discovery result.
type Candidate struct {
	Path       string   `json:"path"`
	Language   string   `json:"language"`
	Digest     string   `json:"digest"`
	Size       int64    `json:"size"`
	Reason     string   `json:"reason"`
	Confidence float64  `json:"confidence"`
	Symbols    []Symbol `json:"symbols,omitempty"`
}

// DiscoverResponse is bounded deterministic code_discover output.
type DiscoverResponse struct {
	Schema      string          `json:"schema"`
	Query       DiscoverRequest `json:"query"`
	IndexDigest string          `json:"indexDigest"`
	Provenance  Provenance      `json:"provenance"`
	Candidates  []Candidate     `json:"candidates"`
	Fallback    bool            `json:"fallback,omitempty"`
}

// GetRequest is exact retrieval by slash path.
type GetRequest struct {
	Path string `json:"path"`
}

// GetResponse is exact code_get retrieval.
type GetResponse struct {
	Schema      string     `json:"schema"`
	Query       GetRequest `json:"query"`
	IndexDigest string     `json:"indexDigest"`
	Provenance  Provenance `json:"provenance"`
	Unit        Unit       `json:"unit"`
	Symbols     []Symbol   `json:"symbols,omitempty"`
	Refs        []Ref      `json:"refs,omitempty"`
}

// Discover returns bounded deterministic candidates with reason/confidence.
// Natural language query is tokenized deterministically; scope filters paths.
// Language-agnostic: unsupported files still score via path. Fallback to bounded inventory when no match.
func Discover(i Index, q DiscoverRequest) (DiscoverResponse, error) {
	if strings.TrimSpace(q.Query) == "" {
		return DiscoverResponse{}, fmt.Errorf("query must be non-empty")
	}
	limit := q.Limit
	if limit == 0 {
		limit = 16
	}
	if limit < 1 || limit > MaxCandidates {
		return DiscoverResponse{}, fmt.Errorf("limit must be 1..%d", MaxCandidates)
	}
	tokens := tokenize(q.Query)
	scope := strings.ToLower(strings.TrimSpace(q.Scope))
	type scored struct {
		cand Candidate
		sc   int
	}
	// Build file->symbols index for reason enrichment.
	fileSymbols := map[string][]Symbol{}
	for _, s := range i.Symbols {
		fileSymbols[s.File] = append(fileSymbols[s.File], s)
	}
	var list []scored
	maxScore := 0
	for _, f := range i.Files {
		if scope != "" && !strings.Contains(strings.ToLower(f.Path), scope) {
			continue
		}
		score := 0
		reasons := []string{}
		lowerPath := strings.ToLower(f.Path)
		for _, tok := range tokens {
			if strings.Contains(lowerPath, tok) {
				score += 3
				reasons = append(reasons, "path match: "+tok)
			}
		}
		// Full query substring boost.
		if strings.Contains(lowerPath, strings.ToLower(q.Query)) {
			score += 2
		}
		// Symbol matches.
		for _, s := range fileSymbols[f.Path] {
			lowerName := strings.ToLower(s.Name)
			for _, tok := range tokens {
				if strings.Contains(lowerName, tok) {
					score += 5
					reasons = append(reasons, "symbol match: "+s.Name)
					break
				}
			}
			if strings.EqualFold(s.Name, q.Query) {
				score += 5
			}
		}
		// Import matches (language-agnostic via refs)
		for _, r := range i.Refs {
			if r.Path == f.Path {
				lowerRef := strings.ToLower(r.Name)
				for _, tok := range tokens {
					if strings.Contains(lowerRef, tok) {
						score += 2
						reasons = append(reasons, "import match: "+r.Name)
						break
					}
				}
			}
		}
		reason := strings.Join(dedup(reasons), ", ")
		if reason == "" && score > 0 {
			reason = "path match"
		}
		c := Candidate{Path: f.Path, Language: f.Language, Digest: f.Digest, Size: f.Size, Reason: reason, Symbols: fileSymbols[f.Path]}
		list = append(list, scored{cand: c, sc: score})
		if score > maxScore {
			maxScore = score
		}
	}
	// Compute confidence normalized to max.
	hasMatch := maxScore > 0
	fallback := !hasMatch
	for n := range list {
		if maxScore > 0 {
			list[n].cand.Confidence = float64(list[n].sc) / float64(maxScore)
			// Round to 2 decimals deterministic.
			list[n].cand.Confidence = float64(int(list[n].cand.Confidence*100+0.5)) / 100
		} else {
			list[n].cand.Confidence = 0
			if list[n].cand.Reason == "" {
				list[n].cand.Reason = "fallback inventory"
			}
		}
		// Cap symbols per candidate to deterministic 8.
		if len(list[n].cand.Symbols) > 8 {
			list[n].cand.Symbols = list[n].cand.Symbols[:8]
		}
	}
	// Sort: score desc, then path asc.
	sort.Slice(list, func(a, b int) bool {
		if list[a].sc != list[b].sc {
			return list[a].sc > list[b].sc
		}
		return list[a].cand.Path < list[b].cand.Path
	})
	cands := []Candidate{}
	for _, s := range list {
		if hasMatch && s.sc == 0 {
			continue
		}
		cands = append(cands, s.cand)
		if len(cands) >= limit {
			break
		}
	}
	// Ambiguity: multiple top-score candidates remain — keep ordered list, confidence ties at 1.
	// Fallback inventory already bounded.
	provenance := Provenance{Source: "local-index", Method: "deterministic-discover", Confidence: 1}
	if fallback {
		provenance.Method = "deterministic-discover+fallback"
		provenance.Confidence = 0.1
	}
	return DiscoverResponse{Schema: "eka/code-discover/1", Query: q, IndexDigest: i.Digest, Provenance: provenance, Candidates: cands, Fallback: fallback}, nil
}

// Get returns exact retrieval for one slash path. Language-agnostic.
func Get(i Index, q GetRequest) (GetResponse, error) {
	p := filepath.ToSlash(filepath.Clean(strings.TrimSpace(q.Path)))
	if p == "" || p == "." {
		return GetResponse{}, fmt.Errorf("path must be non-empty")
	}
	// Disallow traversing outside root.
	if strings.Contains(p, "..") {
		return GetResponse{}, fmt.Errorf("path must not contain ..")
	}
	var found *File
	for n := range i.Files {
		if i.Files[n].Path == p {
			found = &i.Files[n]
			break
		}
	}
	if found == nil {
		return GetResponse{}, fmt.Errorf("code_get: file not found: %q", q.Path)
	}
	b, err := os.ReadFile(filepath.Join(i.Root, filepath.FromSlash(p)))
	content := ""
	if err == nil {
		content = string(b)
	}
	unit := Unit{Path: found.Path, Language: found.Language, Digest: found.Digest, Size: found.Size, Content: content}
	var syms []Symbol
	for _, s := range i.Symbols {
		if s.File == p {
			syms = append(syms, s)
		}
	}
	var refs []Ref
	for _, r := range i.Refs {
		if r.Path == p {
			refs = append(refs, r)
		}
	}
	sort.Slice(syms, func(a, b int) bool { return symbolKey(syms[a]) < symbolKey(syms[b]) })
	sort.Slice(refs, func(a, b int) bool { return refKey(refs[a]) < refKey(refs[b]) })
	return GetResponse{Schema: "eka/code-get/1", Query: q, IndexDigest: i.Digest, Provenance: Provenance{Source: "local-index", Method: "deterministic-get", Confidence: 1}, Unit: unit, Symbols: syms, Refs: refs}, nil
}

func tokenize(s string) []string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.ReplaceAll(s, "/", " ")
	s = strings.ReplaceAll(s, ".", " ")
	fields := strings.Fields(s)
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, " \t\n\r.,;:!\"'`()[]{}")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func dedup(in []string) []string {
	m := map[string]bool{}
	var out []string
	for _, v := range in {
		if !m[v] {
			m[v] = true
			out = append(out, v)
		}
	}
	return out
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
