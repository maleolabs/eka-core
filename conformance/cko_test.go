package conformance_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file tests the CKO-level validation split of the authoring
// workflow: conformance.ValidateCKO (one canonical unit, location rules
// skipped) and conformance.ScanFile (single-file draft classification).
//
// The tests live in the external test package so they can assemble
// exchange.Unit fixtures and project them through the CKOArtifact
// interface (Unit.ToArtifact) — the same wiring the runtime performs.

// ckoUnit builds a valid knowledge-type unit (spec, dimension
// specifications, content-state draft): the shape a freshly scaffolded
// draft produces after version assignment.
func ckoUnit(ns, typ, id string) *exchange.Unit {
	u := &exchange.Unit{
		Identity: exchange.Identity{Namespace: ns, Type: typ, ID: id, InstanceVersion: 1},
		Revision: 1,
		StateVector: exchange.StateVector{
			ContentState:   "draft",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
			{Date: "2026-08-07", Domain: "content-state", From: "-", To: "draft", By: conformance.User("Engineering")},
		},
		Relationships: []exchange.Relationship{},
		Classification: exchange.Classification{
			Dimension: "specifications",
			Domain:    "Architecture",
		},
		Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
		ContentPayload: []byte("# id\n\n## Purpose\n\np\n\n## Content\n\nc\n"),
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	return u
}

// ckoWorkItem builds a valid work-item unit (sto, execution-state todo,
// structured-text content with the required Description/Acceptance
// Criteria sections): the CKO shape of a published work item line.
func ckoWorkItem(ns, id string) *exchange.Unit {
	u := &exchange.Unit{
		Identity: exchange.Identity{Namespace: ns, Type: "sto", ID: id, InstanceVersion: 1},
		Revision: 1,
		StateVector: exchange.StateVector{
			ExecutionState: "todo",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
			{Date: "2026-08-07", Domain: "execution-state", From: "-", To: "todo", By: conformance.User("Engineering")},
		},
		Relationships:  []exchange.Relationship{},
		Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
		ContentPayload: []byte("# id\n\n## Description\n\nd\n\n## Acceptance Criteria\n\nc\n"),
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	return u
}

// ckoValidate validates a unit and returns the report (fatal on a
// validator error — the validator only errors on nil/malformed input).
func ckoValidate(t *testing.T, u *exchange.Unit, opts conformance.ValidateCKOOptions) *conformance.Report {
	t.Helper()
	report, err := conformance.ValidateCKO(u, opts)
	if err != nil {
		t.Fatalf("ValidateCKO: %v", err)
	}
	return report
}

// countR5 counts the report's Rule 5 findings.
func countR5(report *conformance.Report) int {
	n := 0
	for _, r := range report.Results {
		if r.Rule == "R5" {
			n++
		}
	}
	return n
}

// TestValidateCKOValidUnitPasses: a well-formed canonical unit passes
// CKO-level validation with zero findings.
func TestValidateCKOValidUnitPasses(t *testing.T) {
	report := ckoValidate(t, ckoUnit("acme", "spec", "publish-api"), conformance.ValidateCKOOptions{})
	if !report.Pass() {
		t.Errorf("a valid unit must pass, got %d errors: %+v", report.ErrorCount(), report.SortedResults())
	}
	if report.ErrorCount() != 0 || report.WarningCount() != 0 {
		t.Errorf("a valid unit must have 0 findings, got %d errors / %d warnings",
			report.ErrorCount(), report.WarningCount())
	}
	if report.Artifacts != 1 {
		t.Errorf("Artifacts = %d, want 1", report.Artifacts)
	}
}

// TestValidateCKOInvalidStateValue: an invalid state value is a
// blocking Rule 3 finding on the converted artifact.
func TestValidateCKOInvalidStateValue(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ContentState = "bogus"
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	if report.Pass() {
		t.Error("an invalid content-state value must block")
	}
	found := false
	for _, r := range report.Results {
		if r.Rule == "R3" && strings.Contains(r.Message, "bogus") {
			found = true
		}
	}
	if !found {
		t.Errorf("R3 finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOMissingOwnedState: an owned state domain absent from
// the unit's state vector is a blocking Rule 4 finding (owned-set
// compliance is CKO-level).
func TestValidateCKOMissingOwnedState(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ExistenceState = "" // owned by spec-, must be present
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	found := false
	for _, r := range report.Results {
		if r.Rule == "R4" && strings.Contains(r.Message, "missing owned state field existence-state") {
			found = true
		}
	}
	if !found {
		t.Errorf("R4 owned-set finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOUnresolvedRelationshipNonDraft: with no resolver (nil
// Resolve = nothing resolves), an unresolved reference on a NON-draft
// unit is a blocking Rule 5 error.
func TestValidateCKOUnresolvedRelationshipNonDraft(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ContentState = "approved"
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "draft", To: "approved", By: conformance.User("Engineering")},
	}
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost:1"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{Resolve: nil})
	if report.Pass() {
		t.Error("an unresolved reference on a non-draft unit must block")
	}
	found := false
	for _, r := range report.Results {
		if r.Rule == "R5" && r.Severity == conformance.SeverityError &&
			strings.Contains(r.Message, "acme/sto:ghost:1") {
			found = true
		}
	}
	if !found {
		t.Errorf("R5 unresolved-reference finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOUnresolvedRelationshipDraftTolerance: the same
// unresolved reference on a content-state draft unit is a warning only —
// draft tolerance (rule 5) applies to the CKO path.
func TestValidateCKOUnresolvedRelationshipDraftTolerance(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost:1"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{Resolve: nil})
	if !report.Pass() {
		t.Errorf("draft tolerance must not block, got %d errors: %+v", report.ErrorCount(), report.SortedResults())
	}
	if report.WarningCount() != 1 {
		t.Errorf("Warnings = %d, want exactly 1 (the draft-tolerance warning)", report.WarningCount())
	}
}

// TestValidateCKOUnresolvedRelationshipDraftTargetTolerance: the CKO
// path's draft TARGET tolerance — a line-level reference that does not
// resolve but whose target exists as a draft (ValidateCKOOptions.Draft)
// is an allowed draft-to-draft authoring reference: NO finding at all,
// even on a NON-draft unit, where the source-side content-state
// tolerance would not apply.
func TestValidateCKOUnresolvedRelationshipDraftTargetTolerance(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ContentState = "approved"
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "draft", To: "approved", By: conformance.User("Engineering")},
	}
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{
		Resolve: nil,
		Draft: func(ref conformance.Reference) bool {
			return ref.Namespace == "acme" && ref.Type == "sto" && ref.ID == "ghost"
		},
	})
	if !report.Pass() {
		t.Errorf("a draft-target reference must not block, got %d errors: %+v", report.ErrorCount(), report.SortedResults())
	}
	if n := countR5(report); n != 0 {
		t.Errorf("R5 findings = %d, want 0 (draft target tolerance): %+v", n, report.SortedResults())
	}
}

// TestValidateCKOUnresolvedRelationshipDraftTargetAbsent: when the
// draft callback reports the target is NOT a draft, an unresolved
// reference on a non-draft unit remains a blocking R5 error — the
// tolerance is target-specific (published objects referencing missing
// targets still flag).
func TestValidateCKOUnresolvedRelationshipDraftTargetAbsent(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ContentState = "approved"
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "draft", To: "approved", By: conformance.User("Engineering")},
	}
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{
		Draft: func(ref conformance.Reference) bool { return false },
	})
	if report.Pass() {
		t.Error("an unresolved reference on a non-draft target must block")
	}
	found := false
	for _, r := range report.Results {
		if r.Rule == "R5" && r.Severity == conformance.SeverityError &&
			strings.Contains(r.Message, "acme/sto:ghost") {
			found = true
		}
	}
	if !found {
		t.Errorf("R5 unresolved-reference finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOAssignedToDraftTolerance: the assigned-to field rides
// the shared R5 mechanics — an assigned-to target that exists as a
// draft (unpublished) member line is the R5 draft-TARGET tolerance: NO
// finding at all, even on a non-draft work item (a work item may point
// at a draft member line, ADR-029 Decision 2). Work items own no
// content-state, so the source-side content-state tolerance never
// applies to them.
func TestValidateCKOAssignedToDraftTolerance(t *testing.T) {
	u := ckoWorkItem("acme", "login-email")
	u.Relationships = []exchange.Relationship{{Type: "assigned-to", Target: "acme/mbr:ghost"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{
		Draft: func(ref conformance.Reference) bool {
			return ref.Namespace == "acme" && ref.Type == "mbr" && ref.ID == "ghost"
		},
	})
	if !report.Pass() {
		t.Errorf("an assigned-to draft-target reference must not block, got %d errors: %+v", report.ErrorCount(), report.SortedResults())
	}
	if n := countR5(report); n != 0 {
		t.Errorf("R5 findings = %d, want 0 (draft-target tolerance): %+v", n, report.SortedResults())
	}
}

// TestValidateCKOUnresolvedRelationshipDraftTargetVersioned: a
// versioned reference is a claim about a published instance — drafts
// never carry instance versions — so the draft target tolerance does
// not apply and the unresolved versioned reference stays an error.
func TestValidateCKOUnresolvedRelationshipDraftTargetVersioned(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ContentState = "approved"
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "draft", To: "approved", By: conformance.User("Engineering")},
	}
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost:1"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{
		Draft: func(ref conformance.Reference) bool {
			return ref.Type == "sto" && ref.ID == "ghost"
		},
	})
	if report.Pass() {
		t.Error("a versioned reference to a draft line must block (drafts carry no instance versions)")
	}
	found := false
	for _, r := range report.Results {
		if r.Rule == "R5" && r.Severity == conformance.SeverityError &&
			strings.Contains(r.Message, "acme/sto:ghost:1") {
			found = true
		}
	}
	if !found {
		t.Errorf("R5 versioned unresolved-reference finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOResolveTrue: when the resolver reports the reference
// resolves, a non-draft unit passes clean.
func TestValidateCKOResolveTrue(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.StateVector.ContentState = "approved"
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "draft", To: "approved", By: conformance.User("Engineering")},
	}
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost:1"}}

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{
		Resolve: func(ref conformance.Reference) bool { return true },
	})
	if !report.Pass() {
		t.Errorf("a resolving reference must pass, got %d errors: %+v", report.ErrorCount(), report.SortedResults())
	}
}

// TestValidateCKOUnresolvedRelationshipVersioned: a versioned reference
// only resolves when the exact instance exists — the resolver receives
// the parsed Reference and decides; the validator stays
// representation-agnostic.
func TestValidateCKOUnresolvedRelationshipVersioned(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.Relationships = []exchange.Relationship{{Type: "depends-on", Target: "acme/sto:ghost:1"}}

	resolved := conformance.Reference{}
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{
		Resolve: func(ref conformance.Reference) bool {
			resolved = ref
			return false
		},
	})
	if !report.Pass() {
		t.Error("draft tolerance must keep the unit passing")
	}
	if resolved.Namespace != "acme" || resolved.Type != "sto" || resolved.ID != "ghost" {
		t.Errorf("resolver received %+v, want the parsed acme/sto:ghost reference", resolved)
	}
}

// TestValidateCKOLocationRulesNotApplied: the location rules are NOT
// part of the CKO path — a unit whose dimension does not match any
// folder (no AbsPath exists at all) passes, and the canonical form in
// RelPath is not interpreted as a filename (R2).
func TestValidateCKOLocationRulesNotApplied(t *testing.T) {
	u := ckoUnit("acme", "adr", "001")
	u.Classification = exchange.Classification{Dimension: "decisions", Domain: "Architecture"}
	u.StateVector = exchange.StateVector{ContentState: "proposed", ExistenceState: "active"}
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "-", To: "proposed", By: conformance.User("Engineering")},
	}
	u.ContentPayload = []byte("# 001\n\n## Context\n\nc\n\n## Decision\n\nd\n\n## Consequences\n\nc\n\n## Alternatives Considered\n\na\n")
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()

	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	if !report.Pass() {
		t.Errorf("the location rules must not apply to a unit, got %d errors: %+v",
			report.ErrorCount(), report.SortedResults())
	}
	for _, r := range report.Results {
		if r.Rule == "R6" || r.Rule == "R2" {
			t.Errorf("location rule %s must not fire in CKO mode: %s", r.Rule, r.Message)
		}
	}
}

// TestValidateCKOMissingRequiredSection: Rule 9 runs over the converted
// artifact's body lines — a unit whose content lacks a required section
// for its type is blocked.
func TestValidateCKOMissingRequiredSection(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.ContentPayload = []byte("# id\n\n## Purpose\n\np\n") // no ## Content
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	found := false
	for _, r := range report.Results {
		if r.Rule == "R9" && strings.Contains(r.Message, `"Content"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("R9 required-section finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOChangeLogConsistency: Rule 7 runs on the converted
// artifact — the last entry of each owned domain must equal the current
// field value, and every owned domain needs coverage.
func TestValidateCKOChangeLogConsistency(t *testing.T) {
	// Last entry mismatch: the state vector says draft but the last
	// content-state entry ends at approved.
	u := ckoUnit("acme", "spec", "publish-api")
	u.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		{Date: "2026-08-07", Domain: "content-state", From: "-", To: "approved", By: conformance.User("Engineering")},
	}
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	found := false
	for _, r := range report.Results {
		if r.Rule == "R7" && strings.Contains(r.Message, "content-state") &&
			strings.Contains(r.Message, "draft") {
			found = true
		}
	}
	if !found {
		t.Errorf("R7 last-entry finding missing: %+v", report.SortedResults())
	}

	// Missing coverage: no change-log entry for an owned domain.
	u2 := ckoUnit("acme", "spec", "publish-api")
	u2.ChangeLog = []exchange.ChangeLogEntry{
		{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
	}
	report = ckoValidate(t, u2, conformance.ValidateCKOOptions{})
	found = false
	for _, r := range report.Results {
		if r.Rule == "R7" && strings.Contains(r.Message, "content-state") {
			found = true
		}
	}
	if !found {
		t.Errorf("R7 missing-coverage finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOUnknownType: an unknown type token is a blocking R0
// finding (spec §6: identity "token valid").
func TestValidateCKOUnknownType(t *testing.T) {
	u := ckoUnit("acme", "bogus", "x")
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	if report.Pass() {
		t.Error("an unknown type token must block")
	}
	found := false
	for _, r := range report.Results {
		if r.Rule == "R0" && strings.Contains(r.Message, "bogus") {
			found = true
		}
	}
	if !found {
		t.Errorf("R0 unknown-type finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOUnknownRelationshipType: a unit carrying a relationship
// outside the five canonical types is structurally invalid (R5).
func TestValidateCKOUnknownRelationshipType(t *testing.T) {
	u := ckoUnit("acme", "spec", "publish-api")
	u.Relationships = []exchange.Relationship{{Type: "blocks", Target: "acme/sto:x:1"}}
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	found := false
	for _, r := range report.Results {
		if r.Rule == "R5" && strings.Contains(r.Message, "unknown relationship type") {
			found = true
		}
	}
	if !found {
		t.Errorf("R5 unknown-relationship-type finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOEmptyIdentity: an empty namespace or id is a blocking
// R0 finding.
func TestValidateCKOEmptyIdentity(t *testing.T) {
	u := ckoUnit("", "spec", "publish-api")
	report := ckoValidate(t, u, conformance.ValidateCKOOptions{})
	if report.Pass() {
		t.Error("an empty namespace must block")
	}
	found := false
	for _, r := range report.Results {
		if r.Rule == "R0" && strings.Contains(r.Message, "namespace") {
			found = true
		}
	}
	if !found {
		t.Errorf("R0 empty-namespace finding missing: %+v", report.SortedResults())
	}
}

// --- ScanFile ---

// draftFile writes a draft-shaped file: full frontmatter WITHOUT
// instance-version (assigned at publish), plus a body.
func draftFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\nnamespace: acme\n"
	content += "type: sto\nid: my-item\nrevision: 1\n"
	content += "execution-state: planned\nexistence-state: active\n"
	content += "change-log:\n  - date: 2026-08-07\n    domain: existence-state\n    from: \"-\"\n    to: active\n    by: Engineering\n  - date: 2026-08-07\n    domain: execution-state\n    from: \"-\"\n    to: planned\n    by: Engineering\n"
	content += "---\n" + body
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestScanFileDraft: a draft file (no instance-version) classifies as an
// artifact without findings.
func TestScanFileDraft(t *testing.T) {
	path := draftFile(t, t.TempDir(), "sto-my-item.md", "\n# my-item\n\n## Description\n\n## Acceptance Criteria\n")
	a, err := conformance.ScanFile(path)
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if a == nil {
		t.Fatal("a draft must classify as an artifact")
	}
	if a.Type != "sto" || a.ID != "my-item" || a.Namespace != "acme" {
		t.Errorf("artifact identity = %s/%s:%s, want acme/sto:my-item", a.Namespace, a.Type, a.ID)
	}
	if a.InstanceVersion != 0 {
		t.Errorf("InstanceVersion = %d, want 0 (drafts carry none)", a.InstanceVersion)
	}
	if a.States["execution-state"] != "planned" || a.States["existence-state"] != "active" {
		t.Errorf("states = %+v", a.States)
	}
}

// TestScanFileConventionDocument: a file without type/id frontmatter is
// a convention document — nil artifact, no error.
func TestScanFileConventionDocument(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("# README\n\nno frontmatter\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := conformance.ScanFile(path)
	if err != nil {
		t.Fatalf("ScanFile: %v", err)
	}
	if a != nil {
		t.Errorf("a convention document must scan to nil, got %+v", a)
	}
}

// TestScanFileMalformed: a draft with broken frontmatter fails with a
// *ScanError carrying the R0 findings (the parse report of the
// draft-publish edge cases).
func TestScanFileMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sto-broken.md")
	if err := os.WriteFile(path, []byte("---\nnamespace: acme\nnot yaml: [\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := conformance.ScanFile(path)
	if err == nil {
		t.Fatal("a malformed draft must error")
	}
	var se *conformance.ScanError
	if !errors.As(err, &se) {
		t.Fatalf("error = %T, want *conformance.ScanError", err)
	}
	if len(se.Findings) == 0 {
		t.Error("ScanError must carry the structural findings")
	}
	if !strings.Contains(se.Error(), path) {
		t.Errorf("ScanError message must name the file: %q", se.Error())
	}
}

// TestScanFileMissing: an unreadable file is a plain read error.
func TestScanFileMissing(t *testing.T) {
	if _, err := conformance.ScanFile(filepath.Join(t.TempDir(), "nope.md")); err == nil {
		t.Fatal("a missing file must error")
	}
}

// TestValidateCKOTicketContainerResolution (M2): rule 8's container
// check resolves through the same callback semantics as rule 5 — a
// tkt- unit whose derives-from references a resolving "ctr:" target
// passes; an unresolvable one is blocked.
func TestValidateCKOTicketContainerResolution(t *testing.T) {
	ticket := func() *exchange.Unit {
		u := &exchange.Unit{
			Identity:    exchange.Identity{Namespace: "acme", Type: "tkt", ID: "t1", InstanceVersion: 1},
			Revision:    1,
			StateVector: exchange.StateVector{},
			ChangeLog:   []exchange.ChangeLogEntry{},
			Relationships: []exchange.Relationship{
				{Type: "derives-from", Target: "acme/ctr:wave-7"},
			},
			Classification: exchange.Classification{},
			Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
			ContentPayload: []byte("# t1\n\n> Generated \u2014 State Projection. Do NOT edit state here; refresh on read.\n\n## Commands\n\n## Projected Status\n"),
		}
		u.CanonicalIdentityForm = u.Identity.CanonicalForm()
		return u
	}

	// Resolving container: the ticket passes rule 8.
	report := ckoValidate(t, ticket(), conformance.ValidateCKOOptions{
		Resolve: func(ref conformance.Reference) bool {
			return ref.Type == "ctr"
		},
	})
	if !report.Pass() {
		t.Errorf("a ticket deriving from a resolving container must pass, got %d errors: %+v",
			report.ErrorCount(), report.SortedResults())
	}

	// Unresolvable container: rule 8 blocks (the ticket's R5 check would
	// also report it — rule 8's own finding is asserted here).
	report = ckoValidate(t, ticket(), conformance.ValidateCKOOptions{Resolve: nil})
	found := false
	for _, r := range report.Results {
		if r.Rule == "R8" && strings.Contains(r.Message, "container (ctr-) artifact") {
			found = true
		}
	}
	if !found {
		t.Errorf("R8 container finding missing: %+v", report.SortedResults())
	}
}

// TestValidateCKOReplacementSemantics (M2): rule 9's replacement check
// uses the shared resolution semantics. A superseded adr- whose
// replacement resolves (in repository mode the replacement artifact is
// in the index) passes; the CKO-mode limitation is documented in
// rules.go (the unit set holds only the unit itself, so a superseded
// draft cannot verify its replacement and is refused).
func TestValidateCKOReplacementSemantics(t *testing.T) {
	superseded := func() *exchange.Unit {
		u := &exchange.Unit{
			Identity: exchange.Identity{Namespace: "acme", Type: "adr", ID: "001", InstanceVersion: 1},
			Revision: 1,
			StateVector: exchange.StateVector{
				ContentState:   "superseded",
				ExistenceState: "active",
			},
			ChangeLog: []exchange.ChangeLogEntry{
				{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
				{Date: "2026-08-07", Domain: "content-state", From: "accepted", To: "superseded", By: conformance.User("Engineering")},
			},
			Relationships: []exchange.Relationship{},
			Classification: exchange.Classification{
				Dimension: "decisions",
				Domain:    "Architecture",
			},
			Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
			ContentPayload: []byte("# 001\n\n## Context\n\nc\n\n## Decision\n\nd\n\n## Consequences\n\nc\n\n## Alternatives Considered\n\na\n"),
		}
		u.CanonicalIdentityForm = u.Identity.CanonicalForm()
		return u
	}

	// CKO mode: no replacement candidate in the unit set — the
	// superseded unit is refused (documented conservative behavior).
	report := ckoValidate(t, superseded(), conformance.ValidateCKOOptions{
		Resolve: func(ref conformance.Reference) bool { return true },
	})
	found := false
	for _, r := range report.Results {
		if r.Rule == "R9" && strings.Contains(r.Message, "superseded ADR") {
			found = true
		}
	}
	if !found {
		t.Errorf("R9 superseded-replacement finding missing: %+v", report.SortedResults())
	}
}
