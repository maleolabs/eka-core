package conformance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Integration tests against the static fixture trees in testdata/.

const testdataDir = "testdata"

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(testdataDir, name)
}

func TestValidFixtureRepo(t *testing.T) {
	report, err := Validate(fixturePath(t, "valid"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.FilesScanned != 6 {
		t.Errorf("files scanned = %d, want 6 (docs tree only; the fixture README.md at root is not scanned)", report.FilesScanned)
	}
	if report.Artifacts != 6 {
		t.Errorf("artifacts = %d, want 6", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
	// R10 stratification traceability (EKA v1.1): the two ADRs, the plan
	// and the story carry no upward reference chain and are flagged as
	// warnings (never blockers). The container derives-from the plan and
	// passes; the ticket is token-exempt. Warnings: 4 = 2 adr + 1 plan
	// + 1 sto.
	if n := countResults(report, Rule10, SeverityWarning); n != 4 {
		t.Errorf("R10 warnings = %d, want 4:\n%s", n, dumpResults(report))
	}
	if report.WarningCount() != 4 {
		t.Errorf("warnings = %d, want 4:\n%s", report.WarningCount(), dumpResults(report))
	}
	if !report.Pass() {
		t.Error("valid fixture must pass")
	}
}

// TestInvalidFixtures runs every invalid-* fixture tree and asserts that the
// intended rule fires. Presence-based assertions keep the tests robust
// against cascading findings on intentionally broken files.
func TestInvalidFixtures(t *testing.T) {
	cases := []struct {
		dir     string
		want    []expectation // at least one result matching each
		notWant []expectation // no result matching these
	}{
		{
			dir: "invalid-dup-identity",
			want: []expectation{
				{rule: Rule1, sev: SeverityError, sub: "duplicate identity"},
			},
		},
		{
			dir: "invalid-reference",
			want: []expectation{
				{rule: Rule5, sev: SeverityError, sub: "unresolved reference"},
				{rule: Rule5, sev: SeverityWarning, sub: "unresolved reference"},
				{rule: Rule5, sev: SeverityError, sub: "self-reference"},
				{rule: Rule5, sev: SeverityError, sub: "malformed reference"},
			},
		},
		{
			dir: "invalid-state-value",
			want: []expectation{
				{rule: Rule3, sev: SeverityError, sub: "not a valid value"},
				{rule: Rule3, sev: SeverityError, sub: "allowed only on scp-/plan-"},
			},
		},
		{
			dir: "invalid-ownership",
			want: []expectation{
				{rule: Rule4, sev: SeverityError, sub: "not owned by type"},
				{rule: Rule4, sev: SeverityError, sub: "missing owned state field"},
			},
		},
		{
			dir: "invalid-dimension",
			want: []expectation{
				{rule: Rule6, sev: SeverityError, sub: "does not match home folder"},
				{rule: Rule6, sev: SeverityError, sub: "unknown dimension"},
				{rule: Rule6, sev: SeverityError, sub: "missing `dimension`"},
				{rule: Rule6, sev: SeverityError, sub: "not allowed on type"},
			},
		},
		{
			dir: "invalid-filename",
			want: []expectation{
				{rule: Rule2, sev: SeverityError, sub: "does not match frontmatter"},
				{rule: Rule2, sev: SeverityError, sub: "not versioned"},
				{rule: Rule2, sev: SeverityError, sub: "must carry a -v<nn> suffix"},
			},
		},
		{
			dir: "invalid-changelog",
			want: []expectation{
				{rule: Rule7, sev: SeverityError, sub: "last change-log entry"},
				{rule: Rule7, sev: SeverityError, sub: "illegal transition"},
				{rule: Rule7, sev: SeverityError, sub: "not owned by type"},
				{rule: Rule7, sev: SeverityError, sub: "malformed"},
			},
		},
		{
			dir: "invalid-malformed",
			want: []expectation{
				{rule: RuleStructural, sev: SeverityError, sub: "not valid YAML"},
				{rule: RuleStructural, sev: SeverityError, sub: "type XOR id"},
				{rule: RuleStructural, sev: SeverityError, sub: "namespace"},
				{rule: RuleStructural, sev: SeverityError, sub: "instance-version"},
				{rule: RuleStructural, sev: SeverityError, sub: "YYYY-MM-DD"},
			},
		},
		{
			dir: "invalid-sections",
			want: []expectation{
				{rule: Rule9, sev: SeverityError, sub: "missing required content section"},
			},
		},
		{
			dir: "invalid-adr-superseded",
			want: []expectation{
				{rule: Rule9, sev: SeverityError, sub: "must be referenced by a replacement"},
			},
		},
		{
			dir: "invalid-projection",
			want: []expectation{
				{rule: Rule8, sev: SeverityError, sub: "projection header"},
				{rule: Rule8, sev: SeverityWarning, sub: "owner artifact holds"},
			},
			notWant: []expectation{
				{rule: Rule8, sev: SeverityError, sub: "projects"},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			report, err := Validate(fixturePath(t, c.dir))
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if report.Pass() {
				t.Fatalf("fixture %s must fail, got pass", c.dir)
			}
			for _, want := range c.want {
				if !hasExpectation(report, want) {
					t.Errorf("fixture %s: missing expected %s %s %q\nresults:\n%s",
						c.dir, want.sev, want.rule, want.sub, dumpResults(report))
				}
			}
			for _, notWant := range c.notWant {
				if hasExpectation(report, notWant) {
					t.Errorf("fixture %s: unexpected %s %s %q\nresults:\n%s",
						c.dir, notWant.sev, notWant.rule, notWant.sub, dumpResults(report))
				}
			}
		})
	}
}

type expectation struct {
	rule string
	sev  Severity
	sub  string
}

func hasExpectation(report *Report, want expectation) bool {
	for _, r := range report.Results {
		if r.Rule == want.rule && r.Severity == want.sev && strings.Contains(r.Message, want.sub) {
			return true
		}
	}
	return false
}

func dumpResults(report *Report) string {
	var b strings.Builder
	for _, r := range report.SortedResults() {
		b.WriteString("  " + r.String() + "\n")
	}
	return b.String()
}

// TestValidAssignedFixtureRepo: the 'Assigned Work' fixture (ADR-029)
// exercises the full pipeline with an mbr- member file and assigned-to
// frontmatter: the mbr- filename passes R2, the assigned-to reference
// resolves through R5 (and the R13 sub-check passes at done with zero
// notes), and the isolated member line is exempt from R10 stratification
// — the only R10 warnings are the two work items (never exempt).
func TestValidAssignedFixtureRepo(t *testing.T) {
	report, err := Validate(fixturePath(t, "valid-assigned"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.FilesScanned != 3 {
		t.Errorf("files scanned = %d, want 3 (mbr- + two work items)", report.FilesScanned)
	}
	if report.Artifacts != 3 {
		t.Errorf("artifacts = %d, want 3", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
	// R10: exactly the two isolated work items warn; the member line is
	// exempt (ADR-029) despite carrying no upward chain.
	r10Files := map[string]bool{}
	for _, r := range resultsFor(report, Rule10) {
		r10Files[r.File] = true
	}
	for _, file := range []string{
		"docs/operating/work-items/stories/sto-assigned.md",
		"docs/operating/work-items/stories/sto-unassigned.md",
	} {
		if !r10Files[file] {
			t.Errorf("missing R10 warning on non-exempt work item %s\nresults:\n%s", file, dumpResults(report))
		}
	}
	if r10Files["docs/operating/members/mbr-alice.md"] {
		t.Error("unexpected R10 warning on the exempt member line mbr-alice.md")
	}
	// R13: the assigned-to sub-check and the done gate pass for both
	// work items (zero notes, valid same-repository member target).
	if n := countResults(report, Rule13, SeverityError); n != 0 {
		t.Errorf("R13 errors = %d, want 0:\n%s", n, dumpResults(report))
	}
	if !report.Pass() {
		t.Error("valid-assigned fixture must pass")
	}
}

// TestDomainValidFixtureRepo: the Engineering Domain fixture where every
// rule passes — declared domains match, every non-Discovery artifact has
// a resolvable upward chain (direct or transitive), no supersession
// crosses strata. Expect 0 errors and 0 warnings.
func TestDomainValidFixtureRepo(t *testing.T) {
	report, err := Validate(fixturePath(t, "domain-valid"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.Artifacts != 7 {
		t.Errorf("artifacts = %d, want 7", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
	if report.WarningCount() != 0 {
		t.Errorf("warnings = %d, want 0:\n%s", report.WarningCount(), dumpResults(report))
	}
}

// TestDomainInvalidFixtureRepo: the Engineering Domain fixture where
// R10/R11/R12 fire. Presence- and absence-based assertions: R11 unknown
// domain + mismatch errors, R12 upward supersede + amends errors, R10
// warnings on the isolated ctr-/sto- artifacts, and no R10 findings on
// the draft spec or the ticket (both exempt).
func TestDomainInvalidFixtureRepo(t *testing.T) {
	report, err := Validate(fixturePath(t, "domain-invalid"))
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if report.Pass() {
		t.Fatal("fixture must fail (R11/R12 are blocking)")
	}
	for _, want := range []expectation{
		{rule: Rule11, sev: SeverityError, sub: "unknown engineering domain"},
		{rule: Rule11, sev: SeverityError, sub: "does not match the home domain"},
		{rule: Rule12, sev: SeverityError, sub: "supersedes"},
		{rule: Rule12, sev: SeverityError, sub: "amends"},
	} {
		if !hasExpectation(report, want) {
			t.Errorf("missing expected %s %s %q\nresults:\n%s",
				want.sev, want.rule, want.sub, dumpResults(report))
		}
	}
	// R10 findings are file-based (the message does not name the file):
	// the isolated ctr-/sto- artifacts warn, the draft spec and the
	// ticket (both exempt) do not.
	r10Files := map[string]bool{}
	for _, r := range resultsFor(report, Rule10) {
		r10Files[r.File] = true
	}
	for _, file := range []string{
		"docs/operating/work-items/stories/sto-iso-1.md",
		"docs/operating/containers/ctr-gelombang-1.md",
	} {
		if !r10Files[file] {
			t.Errorf("missing R10 warning on %s\nresults:\n%s", file, dumpResults(report))
		}
	}
	for _, file := range []string{
		"docs/specifications/spec-001-draft.md",
		"docs/operating/projections/tkt-sto-iso-1.md",
	} {
		if r10Files[file] {
			t.Errorf("unexpected R10 warning on exempt artifact %s\nresults:\n%s", file, dumpResults(report))
		}
	}
	// Only R11/R12 may produce blocking errors here (the fixture content
	// is otherwise conformant).
	if n := report.ErrorCount(); n != 4 {
		t.Errorf("errors = %d, want exactly 4 (2x R11 + 2x R12):\n%s", n, dumpResults(report))
	}
}

// TestReportDeterminism verifies byte-identical output across runs.
func TestReportDeterminism(t *testing.T) {
	for _, dir := range []string{"valid", "invalid-reference", "invalid-dimension", "invalid-projection"} {
		r1, err := Validate(fixturePath(t, dir))
		if err != nil {
			t.Fatal(err)
		}
		r2, err := Validate(fixturePath(t, dir))
		if err != nil {
			t.Fatal(err)
		}
		s1, s2 := dumpResults(r1), dumpResults(r2)
		if s1 != s2 {
			t.Errorf("%s: results differ between runs:\n%s\nvs\n%s", dir, s1, s2)
		}
	}
}

// TestSortedResultsOrder verifies the documented ordering contract.
func TestSortedResultsOrder(t *testing.T) {
	report, err := Validate(fixturePath(t, "invalid-dimension"))
	if err != nil {
		t.Fatal(err)
	}
	results := report.SortedResults()
	for i := 1; i < len(results); i++ {
		prev, cur := results[i-1], results[i]
		if prev.File > cur.File {
			t.Fatalf("results not sorted by file: %q after %q", cur.File, prev.File)
		}
	}
	// The dimension fixture exercises R6 only; ensure R0..R9 rank ordering
	// does not shuffle files: R6 must appear once per file.
}

// TestScanSkipsTestdataAndHiddenDirs verifies the documented scan policy.
func TestScanSkipsTestdataAndHiddenDirs(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		// A deliberately invalid artifact inside testdata/ must be skipped.
		"testdata/invalid/docs/decisions/adr-bad.md": `---` + "\n" + `type: adr` + "\n---\n",
		".git/invalid.md":                `---` + "\n" + `type: adr` + "\n---\n",
		"docs/decisions/adr-001-good.md": buildADR("eka", "001-good", "accepted", "", ""),
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 1 {
		t.Errorf("files scanned = %d, want 1 (testdata/ and .git skipped)", report.FilesScanned)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
}

// TestValidateInputErrors verifies error handling for bad scan roots.
func TestValidateInputErrors(t *testing.T) {
	if _, err := Validate(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("nonexistent root must error")
	}
	f := filepath.Join(t.TempDir(), "file.md")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(f); err == nil {
		t.Error("file root must error")
	}
}

// TestValidateSkipsWithoutDocsTree: a root without docs/ is SKIPPED, not
// scanned (ADR-018 Decision 2): the report carries the deterministic skip
// note, zero findings and zero artifacts, and passes. A stray
// frontmatter-bearing .md at the root can never fail the repository.
func TestValidateSkipsWithoutDocsTree(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"README.md": "# readme\n",
		// A fully-formed ADR at the root: under the skip semantics it is
		// invisible (no docs/ tree to scan).
		"guide.md": buildADR("eka", "001-x", "accepted", "", ""),
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != skipNoDocsTree {
		t.Errorf("Skipped = %q, want the deterministic skip note %q", report.Skipped, skipNoDocsTree)
	}
	if report.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0", report.FilesScanned)
	}
	if report.Artifacts != 0 {
		t.Errorf("Artifacts = %d, want 0", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
	if !report.Pass() {
		t.Error("a skipped validation must pass")
	}
}

// TestValidateScopesToDocsTreeOnly: with a docs/ tree present, the scan
// covers ONLY docs/ — a stray frontmatter-bearing .md at the repository
// root is not an authoring file and can never raise R2/R6 (or any)
// findings (ADR-018 Decision 2).
func TestValidateScopesToDocsTreeOnly(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		// A fully-formed ADR at the root: wrong filename for its identity
		// (R2) and wrong home folder (R6) — must stay invisible.
		"guide.md":                     buildADR("eka", "001-x", "accepted", "", ""),
		"docs/decisions/adr-001-ok.md": buildADR("eka", "001-ok", "accepted", "", ""),
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Skipped != "" {
		t.Errorf("Skipped = %q, want empty (a real scan ran)", report.Skipped)
	}
	if report.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (docs tree only)", report.FilesScanned)
	}
	if report.Artifacts != 1 {
		t.Errorf("Artifacts = %d, want 1", report.Artifacts)
	}
	for _, r := range report.Results {
		if strings.Contains(r.File, "guide.md") {
			t.Errorf("root-level file must not be scanned: %s", r.String())
		}
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0 (the stray root ADR must not raise R2/R6):\n%s",
			report.ErrorCount(), dumpResults(report))
	}
}

// TestConventionDocumentsAreSkipped verifies docs without type+id are inert.
func TestConventionDocumentsAreSkipped(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"README.md":                    "# readme\n",
		"docs/exchange/validation.md":  "# validation\n",
		"docs/operating/protocol.md":   "# protocol\n",
		"docs/README.md":               "---\ntitle: sources of truth\n---\n\n# docs\n",
		"docs/decisions/adr-001-ok.md": buildADR("eka", "001-ok", "accepted", "", ""),
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.FilesScanned != 4 {
		t.Errorf("files scanned = %d, want 4 (docs tree only; the root README is not scanned)", report.FilesScanned)
	}
	if report.Artifacts != 1 {
		t.Errorf("artifacts = %d, want 1", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("errors = %d, want 0:\n%s", report.ErrorCount(), dumpResults(report))
	}
}

// writeTree creates a temp file tree from a relative path -> content map.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
