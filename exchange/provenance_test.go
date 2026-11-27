package exchange

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/v2/conformance"
	"github.com/maleolabs/eka-core/v2/metadata"
)

// provenance_test.go guards the ADR-035 provenance round trip through the
// exchange layer: the exchange unit is authoritative for provenance during
// transport, so an artifact's declared value must reach the package and come
// back unchanged on import. The failure mode without the builder.go
// projection is silent: every unit exports as "no provenance declared" (the
// human default by absence), so an inferred/reconciled claim disappears the
// moment knowledge leaves its source repository and no consumer can tell the
// difference from a human-authored artifact.
//
// The end-to-end guards below use JSON artifacts because that is the
// representation an import writes; the Markdown reader is guarded separately
// (conformance/envelope_test.go, and the Markdown half of the envelope
// round trip in envelope_test.go), since a Markdown-native repository is
// where a write-only envelope would otherwise go unnoticed.

// provenanceArtifact is one JSON-native story artifact; %s is the artifact id,
// %s the provenance line (empty for the absence case). The story type is used
// because it carries no Knowledge Dimension: the artifact exercises the
// provenance envelope only, not the classification rules.
const provenanceArtifact = `{
  "namespace": "prov",
  "type": "sto",
  "id": "%s",
  "instanceVersion": 1,
  "revision": 1,
  "author": "Marij",
  "created": "2026-11-18",
  "updated": "2026-11-18",
%s  "state": {
    "executionState": "todo",
    "existenceState": "active"
  },
  "changeLog": [
    {"date": "2026-11-18", "domain": "executionState", "from": "-", "to": "todo", "by": "Marij"},
    {"date": "2026-11-18", "domain": "existenceState", "from": "-", "to": "active", "by": "Marij"}
  ],
  "content": {
    "description": "Captured story.",
    "acceptanceCriteria": "The claim survives the exchange."
  }
}
`

// provenanceRepo builds a source repository holding one JSON-native artifact
// with the given provenance declaration ("" omits the field entirely).
func provenanceRepo(t *testing.T, id, provenance string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "src")
	md := metadata.Metadata{Version: 1, Project: "prov", Name: "prov", Namespace: "prov"}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "eka.yaml"), md.Marshal(), 0o644); err != nil {
		t.Fatal(err)
	}
	line := ""
	if provenance != "" {
		line = fmt.Sprintf("  \"provenance\": %q,\n", provenance)
	}
	path := filepath.Join(root, "docs", "operating", "work-items", "stories", "sto-"+id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf(provenanceArtifact, id, line)), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// assertPackagesIdentical requires two exported packages to be byte-equal and
// reports the first differing entries with their bytes (the round-trip
// diagnostic of TestImportRoundTrip, reused so a provenance regression names
// the entries it corrupted instead of only "not identical").
func assertPackagesIdentical(t *testing.T, pkgA, pkgB string) {
	t.Helper()
	dataA, err := os.ReadFile(pkgA)
	if err != nil {
		t.Fatal(err)
	}
	dataB, err := os.ReadFile(pkgB)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(dataA, dataB) {
		return
	}
	entriesA, _ := readZIP(t, pkgA)
	entriesB, _ := readZIP(t, pkgB)
	for name := range entriesA {
		if !bytes.Equal(entriesA[name], entriesB[name]) {
			t.Errorf("entry %s differs between the original and the re-exported package", name)
			t.Errorf("  original: %s", truncate(entriesA[name], 300))
			t.Errorf("  re-export: %s", truncate(entriesB[name], 300))
		}
	}
	t.Fatal("re-export is not byte-identical to the original package (provenance round-trip broken)")
}

// TestImportRoundTripCarriesProvenance: a declared inferred/reconciled
// provenance reaches the exported unit, survives import into a fresh
// repository, and is reproduced byte-identically by the re-export. Both the
// package assertion and the repository assertion are checked: the JSON
// authoring document is where the claim is written back, so it must state the
// value for the next export to read it.
func TestImportRoundTripCarriesProvenance(t *testing.T) {
	for _, provenance := range []string{conformance.ProvenanceInferred, conformance.ProvenanceReconciled} {
		t.Run(provenance, func(t *testing.T) {
			id := "cap-" + provenance
			src := provenanceRepo(t, id, provenance)
			pkgA := exportPackage(t, src)

			identity := Identity{Namespace: "prov", Type: "sto", ID: id, InstanceVersion: 1}
			entries, _ := readZIP(t, pkgA)
			unitJSON, ok := entries[UnitDirName(identity)+"/unit.json"]
			if !ok {
				t.Fatalf("package has no unit entry for %s", identity.CanonicalForm())
			}
			want := `"provenance":"` + provenance + `"`
			if !strings.Contains(string(unitJSON), want) {
				t.Errorf("exported unit does not carry %s:\n%s", want, truncate(unitJSON, 600))
			}

			repoB := initTestRepo(t)
			mustImport(t, pkgA, ImportOptions{Root: repoB})

			imported := filepath.Join(repoB, "docs", "operating", "work-items", "stories", "sto-"+id+".json")
			data, err := os.ReadFile(imported)
			if err != nil {
				t.Fatalf("imported artifact missing: %v", err)
			}
			if !strings.Contains(string(data), `"provenance": "`+provenance+`"`) {
				t.Errorf("imported artifact lost the provenance declaration:\n%s", truncate(data, 600))
			}

			assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
		})
	}
}

// TestImportRoundTripInventsNoProvenance is the absence half of the contract:
// an artifact that declared nothing must not gain a provenance field anywhere
// in the cycle — the human default and absence are the same claim, so writing
// the field would re-spell a default as a declaration and change the bytes of
// every provenance-free artifact.
func TestImportRoundTripInventsNoProvenance(t *testing.T) {
	src := provenanceRepo(t, "cap-none", "")
	pkgA := exportPackage(t, src)

	repoB := initTestRepo(t)
	mustImport(t, pkgA, ImportOptions{Root: repoB})

	data, err := os.ReadFile(filepath.Join(repoB, "docs", "operating", "work-items", "stories", "sto-cap-none.json"))
	if err != nil {
		t.Fatalf("imported artifact missing: %v", err)
	}
	if strings.Contains(string(data), "provenance") {
		t.Fatalf("imported artifact invented a provenance field:\n%s", truncate(data, 600))
	}

	assertPackagesIdentical(t, pkgA, exportPackage(t, repoB))
}

// TestBuildFrontmatterProvenance pins the write side of the legacy Markdown
// rendering: a non-default declaration is emitted, the default and the absence
// are not, and a value outside the enum is refused before anything is staged
// (the Markdown adapter would otherwise refuse the artifact it just
// round-tripped, turning a claim into a validation failure of a file this
// engine wrote).
func TestBuildFrontmatterProvenance(t *testing.T) {
	identity := Identity{Namespace: "prov", Type: "adr", ID: "x", InstanceVersion: 1}
	for _, c := range []struct {
		name       string
		provenance string
		want       bool
	}{
		{"absent", "", false},
		{"human default", conformance.ProvenanceHuman, false},
		{"inferred", conformance.ProvenanceInferred, true},
		{"reconciled", conformance.ProvenanceReconciled, true},
	} {
		u := &Unit{Identity: identity, Revision: 1, Provenance: c.provenance}
		fm, err := buildFrontmatter(u)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := strings.Contains(fm, "provenance"); got != c.want {
			t.Errorf("%s: frontmatter carries provenance = %v, want %v:\n%s", c.name, got, c.want, fm)
		}
	}

	if _, err := buildFrontmatter(&Unit{Identity: identity, Revision: 1, Provenance: "synthetic"}); err == nil {
		t.Error("a provenance outside human|inferred|reconciled must be refused, not written")
	}
}
