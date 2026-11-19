package exchange

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// corpus_test.go is the data-driven transport corpus: testdata/corpus/
// holds one fixture repository per case plus cases.json, which names the
// fields each case declares (or must NOT declare) and pins the round trip to
// byte identity. A new case is a fixture directory and one manifest entry —
// no Go change — so the corpus can grow faster than the bespoke tests and can
// be pointed at by the specification.
//
// The corpus is additive: the hand-written guards (envelope_test.go,
// note_state_test.go, provenance_test.go, conformance/envelope_test.go) stay,
// because they pin diagnostics and refusal paths a fixture-driven round trip
// cannot express.
//
// Fixtures carry no absolute paths and no host state: the directory is copied
// into the standard repository at release time, so it must move cleanly.

// corpusRoot is the corpus directory: fixtures plus cases.json.
const corpusRoot = "testdata/corpus"

// corpusCase is one manifest entry. Dir is relative to corpusRoot.
type corpusCase struct {
	Name string `json:"name"`
	Dir  string `json:"dir"`
	// Expect is embedded rather than named so the manifest stays flat:
	// {"roundtrip": "identical", "unit": [...], "artifact": [...]}.
	Expect corpusExpect `json:"expect"`
}

// corpusExpect is the assertion set of one case.
//
// The two presence lists are separate because the two documents spell the
// envelope differently, and pretending otherwise would either hide a loss or
// assert something false:
//
//   - Unit paths are checked against the exported unit.json. A declared zero
//     confidence is spelled there as "hasConfidence": true only — the value
//     itself is the JSON zero and omitempty drops it, which is exactly why the
//     flag exists.
//   - Artifact paths are checked, by leaf key, against the written authoring
//     document. That document spells the claim as "confidence": 0 and never
//     carries "hasConfidence", which is a package-transport flag, not a
//     repository field.
//
// Absent lists the fields that must appear in no written artifact: absence is
// a claim (the human default), and the cycle must not turn it into a
// declaration.
type corpusExpect struct {
	Roundtrip string   `json:"roundtrip"`
	Unit      []string `json:"unit"`
	Artifact  []string `json:"artifact"`
	Absent    []string `json:"absent"`
}

// loadCorpus decodes the manifest and checks that every entry is sane enough
// to run: a name, a repository-relative directory and the only supported
// round trip mode.
func loadCorpus(t *testing.T) []corpusCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusRoot, "cases.json"))
	if err != nil {
		t.Fatalf("cannot read the corpus manifest: %v", err)
	}
	var cases []corpusCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatalf("corpus manifest is not valid JSON: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("the corpus manifest lists no case; an empty corpus tests nothing")
	}
	return cases
}

// corpusFieldPaths collects every dotted key path of a decoded JSON document.
// Paths walk objects; array elements contribute their paths at the array's
// own path, so a manifest entry names a field without caring how deep or how
// repeated it is.
func corpusFieldPaths(v any, prefix string, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			out[path] = true
			corpusFieldPaths(child, path, out)
		}
	case []any:
		for _, child := range t {
			corpusFieldPaths(child, prefix, out)
		}
	}
}

// corpusLeaf returns the last segment of a manifest field path: the key that
// must survive into a written artifact, whose spelling of a state domain is
// the authoring one (state.noteState) while the unit's is stateVector.*.
func corpusLeaf(path string) string {
	if i := strings.LastIndex(path, "."); i >= 0 {
		return path[i+1:]
	}
	return path
}

// corpusWrittenKeys decodes every JSON file under the repository's docs/ tree
// and returns the set of keys present anywhere in them.
func corpusWrittenKeys(t *testing.T, root string) map[string]bool {
	t.Helper()
	keys := map[string]bool{}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return err
		}
		paths := map[string]bool{}
		corpusFieldPaths(doc, "", paths)
		for p := range paths {
			keys[corpusLeaf(p)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("cannot inspect the written repository: %v", err)
	}
	return keys
}

// TestCorpusRoundTrip runs every manifest case: export the fixture, assert the
// unit carries the fields the case names, import into a fresh repository,
// assert the written artifact still carries them (or, for the absence case,
// that it carries none of them), and require the re-export to be
// byte-identical. The byte-identity leg is what catches a renderer that drops
// a field on the way back: the second package would differ from the first even
// though every explicit presence check passed.
func TestCorpusRoundTrip(t *testing.T) {
	for _, c := range loadCorpus(t) {
		t.Run(c.Name, func(t *testing.T) {
			if c.Expect.Roundtrip != "identical" {
				t.Fatalf("case %s declares roundtrip %q; the corpus only supports \"identical\"", c.Name, c.Expect.Roundtrip)
			}
			if len(c.Expect.Unit) == 0 && len(c.Expect.Artifact) == 0 && len(c.Expect.Absent) == 0 {
				t.Fatalf("case %s expects nothing; a case without an expectation cannot fail", c.Name)
			}

			src := filepath.Join(corpusRoot, c.Dir)
			pkgA := exportPackage(t, src)

			entries, _ := readZIP(t, pkgA)
			unitPaths := map[string]bool{}
			units := 0
			for name, data := range entries {
				if !strings.HasSuffix(name, "/unit.json") {
					continue
				}
				units++
				var doc any
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatalf("case %s: %s is not valid JSON: %v", c.Name, name, err)
				}
				corpusFieldPaths(doc, "", unitPaths)
			}
			if units == 0 {
				t.Fatalf("case %s: the exported package carries no unit", c.Name)
			}
			for _, want := range c.Expect.Unit {
				if !unitPaths[want] {
					t.Errorf("case %s: the exported unit does not carry %s; the export dropped it", c.Name, want)
				}
			}

			repoB := initTestRepo(t)
			mustImport(t, pkgA, ImportOptions{Root: repoB})
			written := corpusWrittenKeys(t, repoB)
			for _, want := range c.Expect.Artifact {
				if !written[corpusLeaf(want)] {
					t.Errorf("case %s: the imported artifact dropped %s; the package carries it and the repository writer loses it", c.Name, want)
				}
			}
			for _, unwanted := range c.Expect.Absent {
				if written[corpusLeaf(unwanted)] {
					t.Errorf("case %s: the imported artifact invented the %s field; absence must not be re-spelled as a declaration", c.Name, unwanted)
				}
			}

			assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
		})
	}
}

// TestCorpusManifestCoversFixtures keeps the corpus honest in both directions:
// every fixture directory is named by the manifest (a dropped entry would
// otherwise stop being tested with no signal), and every entry points at a
// fixture that exists on disk (a renamed directory cannot silently turn into
// an empty case).
func TestCorpusManifestCoversFixtures(t *testing.T) {
	listed := map[string]bool{}
	names := map[string]bool{}
	for _, c := range loadCorpus(t) {
		if c.Name == "" || c.Dir == "" {
			t.Errorf("corpus entry %+v is missing a name or a directory", c)
			continue
		}
		if names[c.Name] {
			t.Errorf("corpus case name %q is declared twice; subtest output could not tell the cases apart", c.Name)
		}
		names[c.Name] = true
		if filepath.IsAbs(c.Dir) {
			t.Errorf("corpus case %q uses the absolute path %q; fixtures must move with the repository", c.Name, c.Dir)
		}
		src := filepath.Join(corpusRoot, c.Dir)
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			t.Errorf("corpus case %q points at %s, which is not a fixture directory", c.Name, src)
			continue
		}
		listed[c.Dir] = true
	}

	entries, err := os.ReadDir(corpusRoot)
	if err != nil {
		t.Fatalf("cannot read %s: %v", corpusRoot, err)
	}
	for _, e := range entries {
		if !e.IsDir() || listed[e.Name()] {
			continue
		}
		t.Errorf("corpus fixture %s is not listed in cases.json; an unlisted fixture is never tested", e.Name())
	}
}
