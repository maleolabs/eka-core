package compile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// fixtureValid is the conformant compile test fixture: 5 artifacts
// (adr-, ctr-, tkt-, sto-, plan-) + 1 attachment (diagram.txt).
const fixtureValid = "testdata/valid"

// TestCompileProducesCKOs: a conformant repository compiles into one
// CKO per artifact, every CKO carrying its Identity, Digest and
// ContentPayload, and the package its integrity digest.
func TestCompileProducesCKOs(t *testing.T) {
	res, err := Compile(fixtureValid)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if res.Package == nil {
		t.Fatal("Result.Package must be set")
	}
	if len(res.CKOs) != 5 {
		t.Errorf("CKOs = %d, want 5 (adr, ctr, tkt, sto, plan)", len(res.CKOs))
	}
	if len(res.CKOs) != len(res.Package.Units) {
		t.Errorf("CKOs (%d) must alias Package.Units (%d)", len(res.CKOs), len(res.Package.Units))
	}
	if res.Validation == nil || !res.Validation.Pass() {
		t.Error("Validation must be present and passing")
	}
	if res.Package.Integrity.PackageDigest == "" {
		t.Error("package digest must be non-empty")
	}

	for _, u := range res.CKOs {
		if u.Identity.Namespace == "" || u.Identity.Type == "" || u.Identity.ID == "" {
			t.Errorf("CKO %+v must carry a complete Identity", u.Identity)
		}
		if u.Digest == "" {
			t.Errorf("CKO %s must carry its per-unit Digest", u.CanonicalIdentityForm)
		}
		if len(u.ContentPayload) == 0 {
			t.Errorf("CKO %s must carry its ContentPayload", u.CanonicalIdentityForm)
		}
		if u.Content.Representation != exchange.StructuredJSON {
			t.Errorf("CKO %s representation = %q, want %q",
				u.CanonicalIdentityForm, u.Content.Representation, exchange.StructuredJSON)
		}
	}
}

// TestCompileFieldMapping verifies the CKO field mapping from the
// authoring representation: state vector, classification, phase and
// change log land on the canonical unit fields.
func TestCompileFieldMapping(t *testing.T) {
	res, err := Compile(fixtureValid)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	byID := map[string]*exchange.Unit{}
	for _, u := range res.CKOs {
		byID[u.Identity.ID] = u
	}

	sto := byID["login-email"]
	if sto == nil {
		t.Fatal("fixture must carry sto:login-email")
	}
	if sto.StateVector.ExecutionState != "in-progress" {
		t.Errorf("sto execution-state = %q, want in-progress", sto.StateVector.ExecutionState)
	}
	if sto.StateVector.ExistenceState != "active" {
		t.Errorf("sto existence-state = %q, want active", sto.StateVector.ExistenceState)
	}
	if sto.StateVector.ContainerState != "" {
		t.Errorf("sto container-state = %q, want absent", sto.StateVector.ContainerState)
	}
	if sto.Classification.Dimension != "operations" {
		t.Errorf("sto dimension = %q, want operations", sto.Classification.Dimension)
	}
	if len(sto.ChangeLog) != 4 {
		t.Errorf("sto change-log = %d entries, want 4", len(sto.ChangeLog))
	}
	last := sto.ChangeLog[len(sto.ChangeLog)-1]
	if last.Domain != "execution-state" || last.From != "todo" || last.To != "in-progress" || last.By.Name != "Engineering Architecture" {
		t.Errorf("sto last change-log entry = %+v, want execution-state todo->in-progress", last)
	}

	plan := byID["roadmap-2026"]
	if plan == nil {
		t.Fatal("fixture must carry plan:roadmap-2026")
	}
	if plan.Phase != "release" {
		t.Errorf("plan phase = %q, want release", plan.Phase)
	}
	if plan.StateVector.PlanningState != "approved" {
		t.Errorf("plan planning-state = %q, want approved", plan.StateVector.PlanningState)
	}
	if plan.Classification.Dimension != "planning" {
		t.Errorf("plan dimension = %q, want planning", plan.Classification.Dimension)
	}
	if plan.Classification.Domain != "Planning" {
		t.Errorf("plan domain = %q, want Planning (derived from the type token)", plan.Classification.Domain)
	}

	// Relationships: derives-from/depends-on targets resolve to the
	// canonical identity form, ordered by (type, target).
	adr := byID["001-runtime"]
	if adr == nil {
		t.Fatal("fixture must carry adr:001-runtime")
	}
	if len(adr.Relationships) != 1 || adr.Relationships[0].Type != "depends-on" {
		t.Fatalf("adr relationships = %+v, want one depends-on", adr.Relationships)
	}
	if adr.Relationships[0].Target != "eka-compile-fixture/sto:login-email:1" {
		t.Errorf("adr depends-on target = %q, want the canonical identity form", adr.Relationships[0].Target)
	}

	tkt := byID["sto-login-email"]
	if tkt == nil {
		t.Fatal("fixture must carry tkt:sto-login-email")
	}
	if len(tkt.Relationships) != 1 || tkt.Relationships[0].Type != "derives-from" ||
		tkt.Relationships[0].Target != "eka-compile-fixture/ctr:gelombang-1:1" {
		t.Errorf("tkt relationships = %+v, want derives-from ctr:gelombang-1", tkt.Relationships)
	}
}

// TestCompileDeterministic: two compiles of identical repository state
// produce identical per-unit digests and identical package digests.
func TestCompileDeterministic(t *testing.T) {
	a, err := Compile(fixtureValid)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	b, err := Compile(fixtureValid)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if a.Package.Integrity.PackageDigest != b.Package.Integrity.PackageDigest {
		t.Errorf("package digest differs between compiles: %q vs %q",
			a.Package.Integrity.PackageDigest, b.Package.Integrity.PackageDigest)
	}
	if len(a.CKOs) != len(b.CKOs) {
		t.Fatalf("CKO counts differ: %d vs %d", len(a.CKOs), len(b.CKOs))
	}
	for i := range a.CKOs {
		if a.CKOs[i].CanonicalIdentityForm != b.CKOs[i].CanonicalIdentityForm {
			t.Errorf("CKO order differs at %d: %q vs %q",
				i, a.CKOs[i].CanonicalIdentityForm, b.CKOs[i].CanonicalIdentityForm)
		}
		if a.CKOs[i].Digest != b.CKOs[i].Digest {
			t.Errorf("unit digest of %s differs between compiles", a.CKOs[i].CanonicalIdentityForm)
		}
	}
}

// TestCompileRefusesNonConformant: a repository with blocking
// violations is refused with *ValidationError carrying the report.
func TestCompileRefusesNonConformant(t *testing.T) {
	dir := t.TempDir()
	bad := "---\nnamespace: eka-compile\n"
	bad += "type: sto\n" // type without id violates the artifact rule (R0)
	bad += "---\n# Bad\n"
	// The conformance scan covers the docs/ knowledge tree only (ADR-018
	// Decision 2): the violating artifact must live under docs/ to be
	// scanned.
	target := filepath.Join(dir, "docs", "operating", "work-items", "sto-bad.md")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Compile(dir)
	if err == nil {
		t.Fatal("non-conformant repository must be refused")
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type = %T, want *ValidationError", err)
	}
	if ve.Report == nil {
		t.Fatal("ValidationError must carry the report")
	}
	if ve.Report.ErrorCount() == 0 {
		t.Error("report must count blocking errors")
	}
	if !strings.Contains(ve.Error(), "no knowledge seeded") {
		t.Errorf("error message must state that no knowledge was seeded, got %q", ve.Error())
	}
	if strings.Contains(ve.Error(), "sync pull refused") {
		t.Errorf("error message must be compiler-generic, not sync-specific, got %q", ve.Error())
	}
}

// TestCompileRefusesMissingRoot: an unreadable root is an error
// wrapped with compile context, not a ValidationError.
func TestCompileRefusesMissingRoot(t *testing.T) {
	_, err := Compile(filepath.Join(t.TempDir(), "missing"))
	if err == nil {
		t.Fatal("missing root must fail")
	}
	if !strings.Contains(err.Error(), "compile:") {
		t.Errorf("error must carry compile context, got %q", err.Error())
	}
}
