package sync

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/compile"
	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// fixtureValid is the conformant sync test fixture (3 artifacts + 1
// attachment, warnings only).
const fixtureValid = "testdata/valid"

// copyFixture copies the fixture tree into a fresh temp dir and
// returns its path.
func copyFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(fixtureValid, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(fixtureValid, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

// newWorkspace sets EKA_HOME to a temp dir and ensures the workspace.
func newWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	t.Setenv("EKA_HOME", t.TempDir())
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatalf("workspace.Ensure: %v", err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

// both returns the default options (pull + push).
func both() Options { return Options{Pull: true, Push: true} }

// TestEKAHomeHonored: the workspace resolves to EKA_HOME.
func TestEKAHomeHonored(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EKA_HOME", home)
	w, err := workspace.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.Path() != filepath.Clean(home) {
		t.Errorf("workspace path = %q, want %q", w.Path(), home)
	}
}

// TestSyncFreshRepo: a first sync registers the repo with the identity
// from eka.yaml (never the basename), seeds the canonical store from
// the snapshot and writes the snapshot package.
func TestSyncFreshRepo(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)

	report, err := Run(w, repoDir, both())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.NewRepo {
		t.Error("first sync must register the repository")
	}
	if report.Project != "eka-sync-fixture" {
		t.Errorf("project = %q, want eka-sync-fixture (from eka.yaml, not the basename)", report.Project)
	}
	if report.Repo != "eka-sync-fixture" {
		t.Errorf("repo = %q, want eka-sync-fixture (from eka.yaml, not the basename)", report.Repo)
	}
	if report.PullSource != "docs" {
		t.Errorf("pull source = %q, want docs (no snapshot yet)", report.PullSource)
	}
	if report.PulledUnits != 4 {
		t.Errorf("pulled units = %d, want 4", report.PulledUnits)
	}
	if report.PulledAttachments != 1 {
		t.Errorf("pulled attachments = %d, want 1", report.PulledAttachments)
	}
	if report.PushedUnits != 4 {
		t.Errorf("pushed units = %d, want 4", report.PushedUnits)
	}
	if report.SnapshotLabel == "" || report.SnapshotDigest == "" {
		t.Errorf("snapshot label/digest must be set: %q / %q", report.SnapshotLabel, report.SnapshotDigest)
	}
	if report.SnapshotLabel != "rsf-repo-eka-sync-fixture-2" {
		t.Errorf("snapshot label = %q", report.SnapshotLabel)
	}

	// Snapshot directory exists with the SOURCE entries only (ADR-027:
	// the derived aggregates are not committed).
	snapshotDir := filepath.Join(repoDir, "exchange", "snapshots")
	for _, want := range []string{"header.json"} {
		if _, err := os.Stat(filepath.Join(snapshotDir, want)); err != nil {
			t.Errorf("snapshot missing %s: %v", want, err)
		}
	}
	for _, gone := range []string{"manifest.json", "declarations.json", "integrity.json"} {
		if _, err := os.Stat(filepath.Join(snapshotDir, gone)); err == nil {
			t.Errorf("snapshot must not carry the derived aggregate %s", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(snapshotDir, "units", "eka-sync-fixture", "adr-001-runtime-v1", "content")); err != nil {
		t.Errorf("snapshot unit entry missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshotDir, "attachments", "docs", "architecture", "diagram.txt")); err != nil {
		t.Errorf("snapshot attachment entry missing: %v", err)
	}
	// The pushed snapshot verifies with the same load a pull applies.
	if _, err := exchange.LoadSnapshot(snapshotDir); err != nil {
		t.Errorf("pushed snapshot refused: %v", err)
	}

	// DB seeded (counts through the store directly — the workspace
	// Counts helper moved to the runtime Knowledge service).
	objects, err := w.Store().RefCount()
	if err != nil {
		t.Fatal(err)
	}
	payloads, err := w.Store().PayloadCount()
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := w.Store().AttachmentCount()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 4 || attachments != 1 {
		t.Errorf("counts = %d objects / %d attachments, want 4 / 1", objects, attachments)
	}
	if payloads != 4 {
		t.Errorf("payloads = %d, want 4 (one immutable payload per unit)", payloads)
	}
	// The stored reference carries its source repo, and the payload
	// preserves the content and the relationships (serialized inside
	// the immutable unit.json). source_repo = the metadata name
	// (ADR-017 D4), stable across renames.
	r, ok, err := w.Store().Ref("eka-sync-fixture/adr:001-runtime:1")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("seeded reference missing")
	}
	if r.SourceRepo != "eka-sync-fixture" {
		t.Errorf("source repo = %q, want eka-sync-fixture (the metadata name)", r.SourceRepo)
	}
	unitJSON, content, err := w.Store().Payload(r.ObjectHash)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "\"context\"") {
		t.Errorf("content not preserved (structured-json payload expected): %q", content)
	}
	u, err := exchange.DecodeUnit(unitJSON, content)
	if err != nil {
		t.Fatalf("stored payload must decode: %v", err)
	}
	if len(u.Relationships) != 1 || u.Relationships[0].Type != "depends-on" ||
		u.Relationships[0].Target != "eka-sync-fixture/sto:login-email:1" {
		t.Errorf("relationships = %+v", u.Relationships)
	}
}

// TestSyncSecondRunIdempotent: the second sync pulls from the snapshot
// (unchanged), re-pushes byte-identical snapshot files, and leaves the
// store untouched.
func TestSyncSecondRunIdempotent(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	snapshotDir := filepath.Join(repoDir, "exchange", "snapshots")
	before := snapshotBytes(t, snapshotDir)

	report, err := Run(w, repoDir, both())
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if !report.Unchanged {
		t.Error("second sync must report unchanged")
	}
	if report.PullSource != "snapshot" {
		t.Errorf("pull source = %q, want snapshot", report.PullSource)
	}
	if report.PulledUnits != 0 {
		t.Errorf("second sync must pull 0 units, got %d", report.PulledUnits)
	}
	if len(report.Warnings) == 0 {
		t.Error("unchanged pull must carry a warning note")
	}

	after := snapshotBytes(t, snapshotDir)
	if !bytesEqualMaps(before, after) {
		t.Error("re-push must produce byte-identical snapshot files")
	}
	objects, err := w.Store().RefCount()
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := w.Store().AttachmentCount()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 4 || attachments != 1 {
		t.Errorf("store changed by second sync: %d objects / %d attachments", objects, attachments)
	}
}

// TestPullCorruptSnapshot: a structurally corrupted snapshot (an
// undecodable unit.json) is refused with an error (integrity class),
// never silently skipped.
func TestPullCorruptSnapshot(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	unitJSON := filepath.Join(repoDir, "exchange", "snapshots", "units", "eka-sync-fixture", "adr-001-runtime-v1", "unit.json")
	data, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitJSON, append([]byte("X"), data[1:]...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(w, repoDir, both()); err == nil {
		t.Fatal("corrupt snapshot must error")
	}
}

// TestMultiRepoOneProject: two repositories registered under one
// project sync into the union in the canonical store; each push
// carries exactly its own objects.
func TestMultiRepoOneProject(t *testing.T) {
	w := newWorkspace(t)
	repoA := copyFixture(t)
	// A second repository with a disjoint namespace (the fixtures must
	// not collide in the canonical store, which is keyed by identity).
	repoB := copyFixture(t)
	rewriteFiles(t, repoB, "eka-sync-fixture", "eka-sync-fixture-b")

	// Both carry eka.yaml: identity project myproject, name = the
	// basename (matching the registration), namespace = the content
	// namespace of each fixture.
	writeEKAFile(t, repoA, "myproject", filepath.Base(repoA), "eka-sync-fixture")
	writeEKAFile(t, repoB, "myproject", filepath.Base(repoB), "eka-sync-fixture-b")

	// Register both under one project.
	_, _, _, err := w.RegisterRepo(repoA, "myproject")
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = w.RegisterRepo(repoB, "myproject")
	if err != nil {
		t.Fatal(err)
	}

	// Sync both.
	for _, repo := range []string{repoA, repoB} {
		report, err := Run(w, repo, both())
		if err != nil {
			t.Fatalf("sync %s: %v", repo, err)
		}
		if report.Project != "myproject" {
			t.Errorf("project = %q, want myproject", report.Project)
		}
		if report.PulledUnits != 4 {
			t.Errorf("pulled units = %d, want 4", report.PulledUnits)
		}
	}

	// Union in the DB: both repos' refs present, one payload each.
	objects, err := w.Store().RefCount()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 8 {
		t.Errorf("union objects = %d, want 8", objects)
	}
	refsA, err := w.Store().Refs("myproject", filepath.Base(repoA))
	if err != nil {
		t.Fatal(err)
	}
	refsB, err := w.Store().Refs("myproject", filepath.Base(repoB))
	if err != nil {
		t.Fatal(err)
	}
	if len(refsA) != 4 || len(refsB) != 4 {
		t.Errorf("per-repo slices = %d/%d, want 4/4", len(refsA), len(refsB))
	}
	for _, r := range refsA {
		if r.SourceRepo != filepath.Base(repoA) {
			t.Errorf("reference %s attributed to %q, want %q", r.Form, r.SourceRepo, filepath.Base(repoA))
		}
	}
	// Each snapshot carries only its own namespace.
	for _, repo := range []string{repoA, repoB} {
		res, err := exchange.LoadSnapshot(filepath.Join(repo, "exchange", "snapshots"))
		if err != nil {
			t.Fatalf("LoadSnapshot(%s): %v", repo, err)
		}
		if len(res.Package.Units) != 4 {
			t.Errorf("snapshot of %s carries %d units, want 4", repo, len(res.Package.Units))
		}
	}
}

// rewriteFiles rewrites one string to another in every file under dir.
func rewriteFiles(t *testing.T, dir, from, to string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), from) {
			return nil
		}
		return os.WriteFile(path, []byte(strings.ReplaceAll(string(data), from, to)), 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMigrationPullSeedsFromDocs: a repository without a snapshot is
// seeded from its docs tree (source "docs").
func TestMigrationPullSeedsFromDocs(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	report, err := Run(w, repoDir, Options{Pull: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.PullSource != "docs" {
		t.Errorf("pull source = %q, want docs", report.PullSource)
	}
	if report.PulledUnits != 4 {
		t.Errorf("pulled units = %d, want 4", report.PulledUnits)
	}
	if report.PushedUnits != 0 {
		t.Errorf("pull-only run must push 0 units, got %d", report.PushedUnits)
	}
	// No snapshot written by a pull-only run.
	if _, err := os.Stat(filepath.Join(repoDir, "exchange", "snapshots")); !os.IsNotExist(err) {
		t.Error("pull-only run must not write a snapshot")
	}
}

// TestFromDocsReseeds: --from-docs re-seeds the canonical store from
// the docs tree even when a snapshot exists, reported as source
// "docs".
func TestFromDocsReseeds(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	report, err := Run(w, repoDir, Options{Pull: true, FromDocs: true})
	if err != nil {
		t.Fatalf("Run with FromDocs: %v", err)
	}
	if report.PullSource != "docs" {
		t.Errorf("pull source = %q, want docs", report.PullSource)
	}
	if report.PulledUnits != 4 {
		t.Errorf("pulled units = %d, want 4", report.PulledUnits)
	}
	// The docs digest differs from the snapshot digest (fresh pull).
	if report.Unchanged {
		t.Error("from-docs pull must not report unchanged")
	}
}

// TestDocsModeValidationGate: a non-conformant repository is refused
// by the docs-mode gate with a compile.ValidationError (the Knowledge
// Compiler's typed validation failure).
func TestDocsModeValidationGate(t *testing.T) {
	w := newWorkspace(t)
	repoDir := t.TempDir()
	dir := filepath.Join(repoDir, "docs", "decisions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeEKAFile(t, repoDir, filepath.Base(repoDir), filepath.Base(repoDir), "eka-sync-fixture")
	bad := "---\nnamespace: x\nid: 1\n---\n# bad\n" // type missing: R0 error
	if err := os.WriteFile(filepath.Join(dir, "bad.md"), []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(w, repoDir, both())
	if err == nil {
		t.Fatal("non-conformant repository must be refused")
	}
	var ve *compile.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error type = %T, want *compile.ValidationError", err)
	}
}

// TestPushOnlyNoObjects: pushing a registered repository with no
// stored objects is a no-op (no files written).
func TestPushOnlyNoObjects(t *testing.T) {
	w := newWorkspace(t)
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeEKAFile(t, repoDir, filepath.Base(repoDir), filepath.Base(repoDir), "eka-sync-fixture")
	if _, _, _, err := w.RegisterRepo(repoDir, ""); err != nil {
		t.Fatal(err)
	}
	report, err := Run(w, repoDir, Options{Push: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.PushedUnits != 0 || report.SnapshotLabel != "" || report.SnapshotDigest != "" {
		t.Errorf("no-op push result = %+v", report)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "exchange", "snapshots")); !os.IsNotExist(err) {
		t.Error("no-op push must not write a snapshot")
	}
}

// snapshotBytes walks the snapshot dir and returns rel path -> bytes.
func snapshotBytes(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// bytesEqualMaps compares two name->bytes maps for byte equality.
func bytesEqualMaps(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for name, data := range a {
		other, ok := b[name]
		if !ok || !bytes.Equal(data, other) {
			return false
		}
	}
	return true
}

// TestSameNameCollisionAcrossProjects (M1 regression, metadata model):
// two repositories with the SAME metadata name registered under
// DIFFERENT projects must never leak objects into each other's
// snapshots — provenance is the (project_id, source_repo) pair, not
// the name alone. Under ADR-017/018 the name comes from eka.yaml (the
// identity pair (project, name) is the registry key); two projects can
// each own a repository named the same.
func TestSameNameCollisionAcrossProjects(t *testing.T) {
	w := newWorkspace(t)
	parent := t.TempDir()
	// Same directory basename under two projects: the repositories are
	// distinguishable only by the (project, name) identity pair.
	repoA := filepath.Join(parent, "proj-a", "repo")
	repoB := filepath.Join(parent, "proj-b", "repo")
	copyFixtureTo(t, repoA)
	copyFixtureTo(t, repoB)
	// Disjoint namespaces: without provenance isolation the second
	// push would package the first repository's units too.
	rewriteFiles(t, repoB, "eka-sync-fixture", "eka-sync-fixture-b")

	// Same metadata name "repo" under different projects; the
	// namespace matches each fixture's content namespace.
	writeEKAFile(t, repoA, "proj-a", "repo", "eka-sync-fixture")
	writeEKAFile(t, repoB, "proj-b", "repo", "eka-sync-fixture-b")

	if _, _, _, err := w.RegisterRepo(repoA, "proj-a"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(repoB, "proj-b"); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{repoA, repoB} {
		if _, err := Run(w, repo, both()); err != nil {
			t.Fatalf("sync %s: %v", repo, err)
		}
	}

	for _, tc := range []struct{ repo, wantNS string }{
		{repoA, "eka-sync-fixture"},
		{repoB, "eka-sync-fixture-b"},
	} {
		res, err := exchange.LoadSnapshot(filepath.Join(tc.repo, "exchange", "snapshots"))
		if err != nil {
			t.Fatalf("LoadSnapshot(%s): %v", tc.repo, err)
		}
		if len(res.Package.Units) != 4 {
			t.Errorf("snapshot of %s carries %d units, want 4 (its own only)", tc.repo, len(res.Package.Units))
		}
		for _, u := range res.Package.Units {
			if u.Identity.Namespace != tc.wantNS {
				t.Errorf("snapshot of %s carries foreign namespace %q", tc.repo, u.Identity.Namespace)
			}
		}
	}
}

// TestAttachmentIsolationAcrossRepos (M5 regression): attachments are
// attributed to their repository; a push never packages another
// repository's attachments into a snapshot.
func TestAttachmentIsolationAcrossRepos(t *testing.T) {
	w := newWorkspace(t)
	parent := t.TempDir()
	repoA := filepath.Join(parent, "repo-a")
	repoB := filepath.Join(parent, "repo-b")
	copyFixtureTo(t, repoA)
	copyFixtureTo(t, repoB)
	rewriteFiles(t, repoB, "eka-sync-fixture", "eka-sync-fixture-b")
	// Remove repo B's own attachment: with provenance isolation its
	// snapshot must carry zero attachments, never repo A's.
	if err := os.Remove(filepath.Join(repoB, "docs", "architecture", "diagram.txt")); err != nil {
		t.Fatal(err)
	}

	// eka.yaml identity matches the registration (project myproject,
	// name = basename) so the sync resolves the same repositories.
	writeEKAFile(t, repoA, "myproject", "repo-a", "eka-sync-fixture")
	writeEKAFile(t, repoB, "myproject", "repo-b", "eka-sync-fixture-b")

	if _, _, _, err := w.RegisterRepo(repoA, "myproject"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(repoB, "myproject"); err != nil {
		t.Fatal(err)
	}
	for _, repo := range []string{repoA, repoB} {
		if _, err := Run(w, repo, both()); err != nil {
			t.Fatalf("sync %s: %v", repo, err)
		}
	}

	attPath := filepath.Join("attachments", "docs", "architecture", "diagram.txt")
	resA, err := exchange.LoadSnapshot(filepath.Join(repoA, "exchange", "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	resB, err := exchange.LoadSnapshot(filepath.Join(repoB, "exchange", "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	if len(resA.Package.Attachments) != 1 {
		t.Errorf("repo A snapshot carries %d attachments, want 1", len(resA.Package.Attachments))
	}
	if len(resB.Package.Attachments) != 0 {
		t.Errorf("repo B snapshot carries %d attachments, want 0 (isolation)", len(resB.Package.Attachments))
	}
	_ = attPath
}

// TestCrossRepoOverwriteRecorded (D4 contract): pulling an identity
// already owned by a different repository with different content
// applies deterministic last-wins AND records the overwrite in the
// report.
func TestCrossRepoOverwriteRecorded(t *testing.T) {
	w := newWorkspace(t)
	parent := t.TempDir()
	repoA := filepath.Join(parent, "repo-a")
	repoB := filepath.Join(parent, "repo-b")
	copyFixtureTo(t, repoA)
	copyFixtureTo(t, repoB)
	// Same namespaces, same identities, different content: change the
	// body of the ADR in repo B so the digests differ.
	adrB := filepath.Join(repoB, "docs", "decisions", "adr-001-runtime.md")
	data, err := os.ReadFile(adrB)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adrB, append(data, []byte("\n# divergent revision\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	// eka.yaml identity matches the registration (project myproject,
	// name = basename) so the sync resolves the same repositories.
	writeEKAFile(t, repoA, "myproject", "repo-a", "eka-sync-fixture")
	writeEKAFile(t, repoB, "myproject", "repo-b", "eka-sync-fixture")

	if _, _, _, err := w.RegisterRepo(repoA, "myproject"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(repoB, "myproject"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(w, repoA, both()); err != nil {
		t.Fatal(err)
	}
	reportB, err := Run(w, repoB, both())
	if err != nil {
		t.Fatal(err)
	}
	if len(reportB.Overwrites) == 0 {
		t.Error("repo B pull must record cross-repository overwrites")
	}
	if !strings.Contains(reportB.Overwrites[0], "001-runtime") {
		t.Errorf("overwrite record = %q, want the colliding identity named", reportB.Overwrites[0])
	}
	if !strings.Contains(reportB.Overwrites[0], "repo-a") {
		t.Errorf("overwrite record = %q, want the previous owner named", reportB.Overwrites[0])
	}

	// Last-wins is symmetric: a forced re-seed of repo A (docs mode
	// never skips) overwrites repo B's divergent copy again — the
	// store keeps the last pull.
	reportA, err := Run(w, repoA, Options{Pull: true, FromDocs: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range reportA.Overwrites {
		if strings.Contains(o, "repo-b") {
			found = true
		}
	}
	if !found {
		t.Errorf("repo A re-pull must overwrite repo B's divergent copy, got %v", reportA.Overwrites)
	}
}

// TestPushFailsOnStoreReadError (M2 regression): a store read failure
// during push must abort the push — a snapshot must never be emitted
// with silently dropped units.
func TestPushFailsOnStoreReadError(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	// Break the store behind the push's back.
	if _, err := w.Store().DB().Exec(`DROP TABLE object_refs`); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(w, repoDir, Options{Push: true}); err == nil {
		t.Fatal("push must fail when reference reads fail")
	}
}

// TestRegisterPathOwnedByFirstProject (m6 regression): a repository
// path is owned by the project that registered it first; registering
// the same path under a different project is refused.
func TestRegisterPathOwnedByFirstProject(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, _, _, err := w.RegisterRepo(repoDir, "proj-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := w.RegisterRepo(repoDir, "proj-two"); err == nil {
		t.Fatal("re-registering an owned path under another project must be refused")
	}
	// Re-registering under the same project stays a no-op.
	_, _, created, err := w.RegisterRepo(repoDir, "proj-one")
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("re-registering the same path under the same project must be a no-op")
	}
}

// copyFixtureTo copies the fixture tree into the target directory.
func copyFixtureTo(t *testing.T, dst string) {
	t.Helper()
	err := filepath.WalkDir(fixtureValid, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(fixtureValid, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestPushReportsSnapshotChanged (M1 regression): when the canonical
// store is tampered behind the runtime's back, the next sync rewrites
// the snapshot with a DIFFERENT digest — the report must say so
// instead of claiming "unchanged", so a user never commits a
// corruption-derived snapshot believing nothing changed.
func TestPushReportsSnapshotChanged(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	first, err := Run(w, repoDir, both())
	if err != nil {
		t.Fatal(err)
	}
	if first.PushChanged {
		t.Error("first sync must not report a snapshot change")
	}

	// Tamper one payload behind the store's back.
	rows, err := w.Store().AllPayloads()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("no payloads seeded")
	}
	if _, err := w.Store().DB().Exec(`UPDATE object_payloads SET content = ? WHERE object_hash = ?`,
		[]byte("tampered"), rows[0].ObjectHash); err != nil {
		t.Fatal(err)
	}

	second, err := Run(w, repoDir, both())
	if err != nil {
		t.Fatal(err)
	}
	if !second.PushChanged {
		t.Error("push after tampering must report the snapshot digest change")
	}
	found := false
	for _, warn := range second.Warnings {
		if strings.Contains(warn, "snapshot rewritten") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings must carry the snapshot-rewritten note, got %v", second.Warnings)
	}
}

// TestPushPopulatesRepoNamespace: after a successful sync (whose push
// resolves the package namespace), the registry records the resolved
// namespace as the repository's default (repos.namespace, schema v3) —
// the resolution rule the authoring commands use for unqualified
// targets inside the repository.
func TestPushPopulatesRepoNamespace(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// The legacy-shaped registration (project/name = basename) is
	// resolved through the metadata identity: overwrite the fixture's
	// own eka.yaml (eka-sync-fixture identity) with the basename
	// identity so the identity lookup hits the registration.
	writeEKAFile(t, repoDir, filepath.Base(repoDir), filepath.Base(repoDir), "eka-sync-fixture")
	if _, _, _, err := w.RegisterRepo(repoDir, ""); err != nil {
		t.Fatal(err)
	}
	// Before any sync the namespace is unset.
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "" {
		t.Errorf("namespace before sync = %q, want \"\"", repo.Namespace)
	}

	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	repo, found, err = w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "eka-sync-fixture" {
		t.Errorf("repo namespace = %q, want eka-sync-fixture (the snapshot/docs namespace)", repo.Namespace)
	}

	// A second sync (idempotent) keeps the recorded namespace.
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	}
	repo, found, err = w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "eka-sync-fixture" {
		t.Errorf("repo namespace after re-sync = %q, want eka-sync-fixture", repo.Namespace)
	}
}

// TestSnapshotExcludesWorkspaceNative (snapshot isolation regression):
// objects published with the "runtime" provenance sentinel (the
// draft-publish workflow) never enter a repository snapshot — the push
// filters by the provenance pair (project_id, source_repo), so
// workspace-native knowledge stays workspace-only. This pins the
// critical isolation property of the authoring workflow.
func TestSnapshotExcludesWorkspaceNative(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// First sync: seeds 4 repo-attributed units + a snapshot.
	if report, err := Run(w, repoDir, both()); err != nil {
		t.Fatal(err)
	} else if report.PushedUnits != 4 {
		t.Fatalf("first sync pushed %d units, want 4", report.PushedUnits)
	}
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatal("FindRepo failed")
	}

	// Seed a workspace-native object (the publish provenance sentinel)
	// into the same project.
	u := &exchange.Unit{
		Identity: exchange.Identity{
			Namespace: "eka-sync-fixture", Type: "sto", ID: "runtime-only", InstanceVersion: 1,
		},
		Revision: 1,
		StateVector: exchange.StateVector{
			ExecutionState: "planned",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-07", Domain: "execution-state", From: "-", To: "planned", By: conformance.User("Engineering")},
			{Date: "2026-08-07", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering")},
		},
		Relationships:  []exchange.Relationship{},
		Classification: exchange.Classification{},
		Content:        exchange.ContentRef{Representation: exchange.ContentRepresentation, File: "content"},
		ContentPayload: []byte("# runtime-only\n\n## Description\n\nd\n\n## Acceptance Criteria\n\nac\n"),
	}
	u.CanonicalIdentityForm = u.Identity.CanonicalForm()
	unitJSON, err := exchange.MarshalUnit(u)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Store().PutUnit(unitJSON, u.ContentPayload, store.Ref{
		Form:            u.CanonicalIdentityForm,
		ProjectID:       repo.ProjectID,
		SourceRepo:      "runtime",
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Dimension:       u.Classification.Dimension,
		Domain:          u.Classification.Domain,
		Phase:           u.Phase,
		UpdatedAt:       "2026-08-07T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// Push again: only the 4 repo-attributed units travel.
	report, err := Run(w, repoDir, Options{Push: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.PushedUnits != 4 {
		t.Fatalf("push carried %d units, want 4 (the workspace-native object must stay out)", report.PushedUnits)
	}
	// The snapshot must not carry the workspace-native form: load the
	// pushed snapshot and check the unit set.
	res, err := exchange.LoadSnapshot(filepath.Join(repoDir, "exchange", "snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range res.Package.Units {
		if strings.Contains(u.CanonicalIdentityForm, "runtime-only") {
			t.Error("the snapshot must not contain workspace-native objects")
		}
	}
	// The unit directory must not exist either.
	if _, err := os.Stat(filepath.Join(repoDir, "exchange", "snapshots", "units", "eka-sync-fixture", "sto-runtime-only-v1")); err == nil {
		t.Error("the snapshot must not carry the workspace-native unit entry")
	}
	// The store keeps the object under the "runtime" provenance.
	refs, err := w.Store().Refs(repo.ProjectID, "runtime")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Form != "eka-sync-fixture/sto:runtime-only:1" {
		t.Errorf("runtime-provenance refs = %+v, want the workspace-native object", refs)
	}
}

// writeEKAFile writes an eka.yaml with the given identity triple into
// dir (the ADR-017 identity file).
func writeEKAFile(t *testing.T, dir, project, name, namespace string) {
	t.Helper()
	content := "version: 1\nproject: " + project + "\nname: " + name + "\nnamespace: " + namespace + "\n"
	if err := os.WriteFile(filepath.Join(dir, "eka.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSyncAutoRegistersFromMetadata: a first sync of a repository with
// eka.yaml registers the identity from the file (project + name, never
// the basename) and records the namespace immediately.
func TestSyncAutoRegistersFromMetadata(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// The content must carry the declared namespace (ADR-020
	// reconciliation): rewrite the fixture's docs to atrium-api.
	rewriteFiles(t, repoDir, "eka-sync-fixture", "atrium-api")
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-api")

	report, err := Run(w, repoDir, both())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !report.NewRepo {
		t.Error("first sync must register the repository")
	}
	if report.Project != "atrium" {
		t.Errorf("project = %q, want atrium (from eka.yaml, not the basename)", report.Project)
	}
	if report.Repo != "api" {
		t.Errorf("repo = %q, want api (from eka.yaml, not the basename)", report.Repo)
	}

	// The namespace is recorded in the registry (repos.namespace).
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "atrium-api" {
		t.Errorf("repo namespace = %q, want atrium-api (from eka.yaml)", repo.Namespace)
	}
	if repo.Name != "api" || repo.ProjectID != "atrium" {
		t.Errorf("repo = %+v, want atrium/api", repo)
	}
}

// TestSyncMetadataRegistryMismatch: a path registered under a DIFFERENT
// project, then synced with eka.yaml whose identity is not registered —
// the metadata registration is refused by the path-ownership rule, with
// the owning project named in the error.
func TestSyncMetadataRegistryMismatch(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, _, _, err := w.RegisterRepo(repoDir, "other"); err != nil {
		t.Fatal(err)
	}
	// The content must carry the declared namespace (ADR-020
	// reconciliation): the reconciliation detection runs BEFORE the
	// registration resolution, so a content/declared mismatch would
	// refuse with the override hint and never reach the path-ownership
	// gate this test pins.
	rewriteFiles(t, repoDir, "eka-sync-fixture", "atrium-api")
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-api")

	_, err := Run(w, repoDir, both())
	if err == nil {
		t.Fatal("the identity mismatch must refuse the sync")
	}
	if !strings.Contains(err.Error(), `already registered under project "other"`) {
		t.Errorf("refusal error = %v, want the owning project named", err)
	}
}

// TestSyncNamespaceImmutable: after the first sync the identity pair is
// frozen — when the eka.yaml namespace AND the content drift together
// (the reconciliation detection passes: content == declared), the
// registered row's namespace mismatch is refused with the deterministic
// immutability error.
func TestSyncNamespaceImmutable(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// The content must carry the declared namespace (ADR-020
	// reconciliation): rewrite the fixture's docs to atrium-api.
	rewriteFiles(t, repoDir, "eka-sync-fixture", "atrium-api")
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-api")
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// The repository drifts to a NEW namespace in both the file and
	// the content (the drift of file AND content together passes the
	// content reconciliation detection, which runs before the
	// registration resolution — then the frozen identity refuses).
	// The first-run snapshot is removed: with the snapshot present the
	// detection reads the OLD content namespace from the package and
	// refuses earlier with the content-mismatch hint instead (see the
	// file-drift sub-test below).
	if err := os.RemoveAll(filepath.Join(repoDir, "exchange")); err != nil {
		t.Fatal(err)
	}
	rewriteFiles(t, repoDir, "atrium-api", "atrium-mobile")
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-mobile")
	_, err := Run(w, repoDir, both())
	if err == nil {
		t.Fatal("a namespace drift must be refused")
	}
	if !strings.Contains(err.Error(), "frozen") {
		t.Errorf("error = %v, want the immutability refusal (frozen)", err)
	}
	if !strings.Contains(err.Error(), "atrium-mobile") || !strings.Contains(err.Error(), "atrium-api") {
		t.Errorf("error = %v, want both namespace values named", err)
	}
	if !strings.Contains(err.Error(), "sync refused:") {
		t.Errorf("error = %v, want the sync refused prefix", err)
	}
	// The refusal writes nothing: the registered row keeps the frozen
	// namespace and no units were (re)seeded.
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "atrium-api" {
		t.Errorf("repo namespace after refusal = %q, want the frozen atrium-api (no write)", repo.Namespace)
	}
	objects, err := w.Store().RefCount()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 4 {
		t.Errorf("store objects after refusal = %d, want 4 (no re-seed)", objects)
	}
}

// TestSyncFileNamespaceDriftRefused (ADR-020 D3): detection runs
// BEFORE the registration resolution and any store mutation — a
// registered repository whose eka.yaml namespace alone drifts from the
// content is refused by the reconciliation detection itself (the
// content namespace differs from the DECLARED one), with the override
// hint, and the store stays untouched.
func TestSyncFileNamespaceDriftRefused(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// The content must carry the declared namespace (ADR-020
	// reconciliation): rewrite the fixture's docs to atrium-api.
	rewriteFiles(t, repoDir, "eka-sync-fixture", "atrium-api")
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-api")
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Edit ONLY the file: same identity, different namespace. The
	// content (docs AND the first-run snapshot) still resolves to
	// atrium-api — the detection refuses the drift with the override
	// hint before the immutability check ever runs.
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-mobile")
	_, err := Run(w, repoDir, both())
	if err == nil {
		t.Fatal("a namespace drift must be refused")
	}
	want := "sync refused: the repository content namespace atrium-api differs from the registered repository namespace atrium-mobile; run 'eka sync --override' to align the repository identity to atrium-api"
	if err.Error() != want {
		t.Errorf("refusal = %q, want the pinned byte-exact string %q", err.Error(), want)
	}
	// The refusal writes nothing: no registration change, no re-seed.
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "atrium-api" {
		t.Errorf("repo namespace after refusal = %q, want the frozen atrium-api (no write)", repo.Namespace)
	}
	parsed, err := metadata.Parse(mustRead(t, filepath.Join(repoDir, "eka.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Namespace != "atrium-mobile" {
		t.Errorf("eka.yaml namespace after refusal = %q, want atrium-mobile (no rewrite)", parsed.Namespace)
	}
	objects, err := w.Store().RefCount()
	if err != nil {
		t.Fatal(err)
	}
	if objects != 4 {
		t.Errorf("store objects after refusal = %d, want 4 (no re-seed)", objects)
	}
}

// TestSyncPushNamespaceFromMetadata: the emitted snapshot header carries
// the eka.yaml namespace (the metadata namespace is the package
// namespace, resolved before the legacy snapshot-header/most-common
// order).
func TestSyncPushNamespaceFromMetadata(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// The content must carry the declared namespace (ADR-020
	// reconciliation): rewrite the fixture's docs to atrium-api.
	rewriteFiles(t, repoDir, "eka-sync-fixture", "atrium-api")
	writeEKAFile(t, repoDir, "atrium", "api", "atrium-api")
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repoDir, "exchange", "snapshots", "header.json"))
	if err != nil {
		t.Fatalf("read header.json: %v", err)
	}
	var header struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatalf("header.json is not JSON: %v", err)
	}
	if header.Namespace != "atrium-api" {
		t.Errorf("snapshot header namespace = %q, want atrium-api (from eka.yaml)", header.Namespace)
	}
}

// TestSyncWorktreeResolvesSameRepo (the ADR-017 worktree scenario): a
// worktree at a different path carrying the SAME eka.yaml resolves to
// the same repository — same identity, no re-registration, the stored
// path refreshed to the worktree, and the source_repo provenance stays
// the metadata name.
func TestSyncWorktreeResolvesSameRepo(t *testing.T) {
	w := newWorkspace(t)
	parent := t.TempDir()
	repoX := filepath.Join(parent, "repo")
	worktreeY := filepath.Join(parent, "wt")
	copyFixtureTo(t, repoX)
	copyFixtureTo(t, worktreeY)
	// The content must carry the declared namespace (ADR-020
	// reconciliation): rewrite both fixtures' docs to atrium-api.
	rewriteFiles(t, repoX, "eka-sync-fixture", "atrium-api")
	rewriteFiles(t, worktreeY, "eka-sync-fixture", "atrium-api")
	writeEKAFile(t, repoX, "atrium", "api", "atrium-api")
	writeEKAFile(t, worktreeY, "atrium", "api", "atrium-api")

	// Register + sync at the repo root X.
	first, err := Run(w, repoX, both())
	if err != nil {
		t.Fatalf("Run(X): %v", err)
	}
	if !first.NewRepo || first.Project != "atrium" || first.Repo != "api" {
		t.Fatalf("first report = %+v, want new repo atrium/api", first)
	}

	// Sync from the worktree Y: same identity, no new registration,
	// path refreshed to Y.
	second, err := Run(w, worktreeY, both())
	if err != nil {
		t.Fatalf("Run(Y): %v", err)
	}
	if second.NewRepo {
		t.Error("the worktree must NOT re-register the repository")
	}
	if second.Project != "atrium" || second.Repo != "api" {
		t.Errorf("worktree report = %+v, want the same identity atrium/api", second)
	}

	got, found, err := w.FindRepo(worktreeY)
	if err != nil || !found {
		t.Fatalf("FindRepo(Y) = %v, %v", found, err)
	}
	if got.Path != filepath.Clean(worktreeY) {
		t.Errorf("stored path = %q, want the worktree path %q (refreshed)", got.Path, filepath.Clean(worktreeY))
	}

	// source_repo provenance is stable: all units attributed to "api".
	refs, err := w.Store().Refs("atrium", "api")
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 4 {
		t.Errorf("refs = %d, want 4", len(refs))
	}
	for _, r := range refs {
		if r.SourceRepo != "api" {
			t.Errorf("reference %s attributed to %q, want api", r.Form, r.SourceRepo)
		}
	}
}

// TestSyncFromSubdirectorySyncsRepositoryRoot (BLOCKER regression): a
// sync run from a SUBDIRECTORY of a metadata repository must sync the
// walk-up repository root — the registry path stays the root, the
// snapshot is written to <root>/exchange/snapshots (never
// <subdir>/exchange/), and a repeat run from the subdir is an
// idempotent no-op. The previous behavior discarded the walk-up
// directory and re-pointed the registry at the subdir, corrupting
// state.
func TestSyncFromSubdirectorySyncsRepositoryRoot(t *testing.T) {
	// (a) Fresh scratch store: register + sync at the fixture root.
	w := newWorkspace(t)
	root := copyFixture(t)
	report, err := Run(w, root, both())
	if err != nil {
		t.Fatalf("Run(root): %v", err)
	}
	if !report.NewRepo {
		t.Error("first sync at the root must register the repository")
	}
	repo, found, err := w.FindRepo(root)
	if err != nil || !found {
		t.Fatalf("FindRepo(root) = %v, %v", found, err)
	}
	if repo.Path != root {
		t.Errorf("stored path = %q, want the repository root %q", repo.Path, root)
	}

	// (b) Sync from a subdirectory: same repository, no re-registration,
	// the registry path stays the root (never the subdir), and the
	// snapshot lives at the root — no stray docs/exchange/ tree.
	subdir := filepath.Join(root, "docs")
	report, err = Run(w, subdir, both())
	if err != nil {
		t.Fatalf("Run(<root>/docs): %v", err)
	}
	if report.NewRepo {
		t.Error("a subdirectory sync must NOT re-register the repository")
	}
	if report.Project != "eka-sync-fixture" || report.Repo != "eka-sync-fixture" {
		t.Errorf("subdir report = %+v, want the same identity eka-sync-fixture", report)
	}
	for _, lookup := range []string{root, subdir} {
		got, found, err := w.FindRepo(lookup)
		if err != nil || !found {
			t.Fatalf("FindRepo(%q) = %v, %v", lookup, found, err)
		}
		if got.Path != root {
			t.Errorf("FindRepo(%q).Path = %q, want the repository root %q (a subdir sync must not re-point the registry)", lookup, got.Path, root)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "exchange", "snapshots", "header.json")); err != nil {
		t.Errorf("snapshot must be written to <root>/exchange/snapshots: %v", err)
	}
	if _, err := os.Stat(filepath.Join(subdir, "exchange")); !os.IsNotExist(err) {
		t.Error("a subdir sync must not create <subdir>/exchange/")
	}

	// A repeat run from the subdir is an idempotent no-op.
	report, err = Run(w, subdir, both())
	if err != nil {
		t.Fatalf("second Run(<root>/docs): %v", err)
	}
	if !report.Unchanged {
		t.Error("a repeat subdir sync must report unchanged")
	}

	// (c) Fresh-store variant: the FIRST-EVER sync from the subdir
	// registers the repository at the root path, never the subdir.
	w2 := newWorkspace(t)
	root2 := copyFixture(t)
	report, err = Run(w2, filepath.Join(root2, "docs"), both())
	if err != nil {
		t.Fatalf("fresh-store Run(<root>/docs): %v", err)
	}
	if !report.NewRepo {
		t.Error("the first-ever subdir sync must register the repository")
	}
	if report.Project != "eka-sync-fixture" || report.Repo != "eka-sync-fixture" {
		t.Errorf("fresh-store subdir report = %+v, want the identity eka-sync-fixture", report)
	}
	repo, found, err = w2.FindRepo(root2)
	if err != nil || !found {
		t.Fatalf("FindRepo(root2) = %v, %v", found, err)
	}
	if repo.Path != root2 {
		t.Errorf("fresh-store registration path = %q, want the repository root %q (never the subdir)", repo.Path, root2)
	}
	if _, err := os.Stat(filepath.Join(root2, "exchange", "snapshots", "header.json")); err != nil {
		t.Errorf("snapshot must be written to <root>/exchange/snapshots: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root2, "docs", "exchange")); !os.IsNotExist(err) {
		t.Error("a subdir sync must not create <subdir>/exchange/")
	}
}

// TestSyncRefusesWithoutMetadata (ADR-018): a directory whose walk-up
// finds no eka.yaml is NOT an EKA repository — the sync is refused
// deterministically with the pinned refusal sentence, regardless of
// registration state. The legacy basename auto-registration is gone.
func TestSyncRefusesWithoutMetadata(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// Remove the fixture's eka.yaml: the tree stops being an EKA
	// repository.
	if err := os.Remove(filepath.Join(repoDir, "eka.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, _, hasMeta, err := metadata.Find(repoDir); err != nil || hasMeta {
		t.Fatalf("metadata.Find = hasMeta %v, %v; want no metadata (gate path)", hasMeta, err)
	}
	_, err := Run(w, repoDir, both())
	if err == nil {
		t.Fatal("sync without eka.yaml must be refused")
	}
	if !strings.Contains(err.Error(), "is not an EKA repository (no eka.yaml)") {
		t.Errorf("refusal error = %v, want the pinned ADR-018 sentence", err)
	}
	if !strings.Contains(err.Error(), "run 'eka init' first") {
		t.Errorf("refusal error = %v, want the init hint", err)
	}
	// The refusal must not have registered anything.
	projects, err := w.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 0 {
		t.Errorf("a refused sync must not register anything, got %+v", projects)
	}
	// The refusal applies to a registered repository too: registration
	// never bypasses the repository-context gate.
	if _, _, _, err := w.RegisterRepo(repoDir, ""); err != nil {
		t.Fatal(err)
	}
	_, err = Run(w, repoDir, both())
	if err == nil {
		t.Fatal("sync of a registered directory without eka.yaml must be refused")
	}
	if !strings.Contains(err.Error(), "is not an EKA repository (no eka.yaml)") {
		t.Errorf("refusal error = %v, want the pinned ADR-018 sentence", err)
	}
	// The refused run added nothing to the registry.
	projects, err = w.Projects()
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Errorf("the refused sync must not add registrations, got %+v", projects)
	}
}

// TestSyncPullPushRefuseWithoutMetadata (ADR-018): the pull-only and
// push-only modes go through the same gate.
func TestSyncPullPushRefuseWithoutMetadata(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if err := os.Remove(filepath.Join(repoDir, "eka.yaml")); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []Options{{Pull: true}, {Push: true}} {
		_, err := Run(w, repoDir, opts)
		if err == nil {
			t.Fatalf("Run(%+v) without eka.yaml must be refused", opts)
		}
		if !strings.Contains(err.Error(), "is not an EKA repository (no eka.yaml)") {
			t.Errorf("refusal error = %v, want the pinned ADR-018 sentence", err)
		}
	}
}

// TestSyncContentNamespaceMismatchRefused (ADR-020): content resolving
// to exactly ONE distinct namespace differing from the registered one
// is refused deterministically with the pinned sentence — identity
// untouched (no eka.yaml rewrite, no snapshot).
func TestSyncContentNamespaceMismatchRefused(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// The declared namespace drifts from the content (eka-sync-fixture).
	writeEKAFile(t, repoDir, "eka-sync-fixture", "eka-sync-fixture", "other")

	_, err := Run(w, repoDir, both())
	if err == nil {
		t.Fatal("a content/repository namespace mismatch must refuse the sync")
	}
	want := "sync refused: the repository content namespace eka-sync-fixture differs from the registered repository namespace other; run 'eka sync --override' to align the repository identity to eka-sync-fixture"
	if err.Error() != want {
		t.Errorf("refusal = %q, want the pinned byte-exact string %q", err.Error(), want)
	}
	// The refusal leaves the identity untouched: eka.yaml unchanged, no
	// snapshot written.
	parsed, err := metadata.Parse(mustRead(t, filepath.Join(repoDir, "eka.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Namespace != "other" {
		t.Errorf("eka.yaml namespace after refusal = %q, want other (no rewrite)", parsed.Namespace)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "exchange", "snapshots")); !os.IsNotExist(err) {
		t.Error("a refused sync must not write a snapshot")
	}
	// The refusal leaves the STORE untouched (ADR-020 D3: detection
	// runs before any store mutation — no registration row, no seeded
	// units).
	if _, found, err := w.FindRepo(repoDir); err != nil || found {
		t.Errorf("a refused sync must not register the repository (found = %v, err = %v)", found, err)
	}
	units, err := w.Store().Units("eka-sync-fixture", "eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Errorf("a refused sync must not seed units, got %d", len(units))
	}
}

// TestSyncOverrideAlignsIdentity (ADR-020): --override aligns the
// repository identity to the content namespace — repos.namespace
// updated, eka.yaml rewritten (project/name untouched), the aligned
// note in the warnings, and the push packages under the aligned
// namespace.
func TestSyncOverrideAlignsIdentity(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	writeEKAFile(t, repoDir, "eka-sync-fixture", "eka-sync-fixture", "other")

	report, err := Run(w, repoDir, Options{Pull: true, Push: true, Override: true})
	if err != nil {
		t.Fatalf("Run with Override: %v", err)
	}
	// repos.namespace aligned to the content namespace (identity
	// lookup).
	repo, found, err := w.FindRepo(repoDir)
	if err != nil || !found {
		t.Fatalf("FindRepo = %v, %v", found, err)
	}
	if repo.Namespace != "eka-sync-fixture" {
		t.Errorf("repo namespace = %q, want eka-sync-fixture (aligned to the content)", repo.Namespace)
	}
	// eka.yaml rewritten: namespace aligned, project/name untouched.
	parsed, err := metadata.Parse(mustRead(t, filepath.Join(repoDir, "eka.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Namespace != "eka-sync-fixture" {
		t.Errorf("eka.yaml namespace = %q, want eka-sync-fixture (rewritten)", parsed.Namespace)
	}
	if parsed.Project != "eka-sync-fixture" || parsed.Name != "eka-sync-fixture" {
		t.Errorf("eka.yaml identity = %+v, want project/name untouched", parsed)
	}
	// The aligned note is a warning, byte-pinned.
	note := "repository namespace aligned: other → eka-sync-fixture (eka.yaml updated; identity frozen again)"
	foundNote := false
	for _, wl := range report.Warnings {
		if wl == note {
			foundNote = true
		}
	}
	if !foundNote {
		t.Errorf("warnings must carry the aligned note %q, got %v", note, report.Warnings)
	}
	// The push packaged under the aligned namespace: header + label.
	data, err := os.ReadFile(filepath.Join(repoDir, "exchange", "snapshots", "header.json"))
	if err != nil {
		t.Fatalf("read header.json: %v", err)
	}
	var header struct {
		Namespace string `json:"namespace"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatalf("header.json is not JSON: %v", err)
	}
	if header.Namespace != "eka-sync-fixture" {
		t.Errorf("snapshot header namespace = %q, want the aligned eka-sync-fixture", header.Namespace)
	}
	if report.SnapshotLabel != "rsf-repo-eka-sync-fixture-2" {
		t.Errorf("snapshot label = %q, want rsf-repo-eka-sync-fixture-2 (aligned namespace)", report.SnapshotLabel)
	}
	// A second sync (identity frozen again) is consistent: no refusal,
	// no re-alignment note.
	report2, err := Run(w, repoDir, both())
	if err != nil {
		t.Fatalf("second Run after alignment: %v", err)
	}
	if !report2.Unchanged {
		t.Error("second sync after alignment must report unchanged")
	}
	for _, wl := range report2.Warnings {
		if strings.HasPrefix(wl, "repository namespace aligned:") {
			t.Errorf("a consistent sync must not realign, got warning %q", wl)
		}
	}
}

// TestSyncConfirmAlignsIdentity (ADR-020): the injected Confirm
// callback drives the interactive path — choosing "align identity to
// <contentNS>" aligns; choosing "abort" yields the same deterministic
// refusal.
func TestSyncConfirmAlignsIdentity(t *testing.T) {
	t.Run("confirm align", func(t *testing.T) {
		w := newWorkspace(t)
		repoDir := copyFixture(t)
		writeEKAFile(t, repoDir, "eka-sync-fixture", "eka-sync-fixture", "other")
		var gotPrompt string
		var gotOptions []string
		var gotDefault int
		opts := both()
		opts.Confirm = func(prompt string, options []string, defaultIdx int) (int, error) {
			gotPrompt = prompt
			gotOptions = options
			gotDefault = defaultIdx
			return 0, nil
		}
		report, err := Run(w, repoDir, opts)
		if err != nil {
			t.Fatalf("Run with Confirm(align): %v", err)
		}
		if gotPrompt != "the repository content namespace eka-sync-fixture differs from the registered repository namespace other" {
			t.Errorf("prompt = %q", gotPrompt)
		}
		if len(gotOptions) != 2 || gotOptions[0] != "align identity to eka-sync-fixture" || gotOptions[1] != "abort" {
			t.Errorf("options = %v, want [align identity to eka-sync-fixture abort]", gotOptions)
		}
		if gotDefault != 0 {
			t.Errorf("default index = %d, want 0 (align)", gotDefault)
		}
		repo, found, err := w.FindRepo(repoDir)
		if err != nil || !found {
			t.Fatalf("FindRepo = %v, %v", found, err)
		}
		if repo.Namespace != "eka-sync-fixture" {
			t.Errorf("repo namespace = %q, want the aligned eka-sync-fixture", repo.Namespace)
		}
		foundNote := false
		for _, wl := range report.Warnings {
			if strings.HasPrefix(wl, "repository namespace aligned:") {
				foundNote = true
			}
		}
		if !foundNote {
			t.Errorf("warnings must carry the aligned note, got %v", report.Warnings)
		}
	})
	t.Run("confirm abort", func(t *testing.T) {
		w := newWorkspace(t)
		repoDir := copyFixture(t)
		writeEKAFile(t, repoDir, "eka-sync-fixture", "eka-sync-fixture", "other")
		opts := both()
		opts.Confirm = func(prompt string, options []string, defaultIdx int) (int, error) {
			return 1, nil
		}
		_, err := Run(w, repoDir, opts)
		if err == nil {
			t.Fatal("a Confirm abort must refuse the sync")
		}
		want := "sync refused: the repository content namespace eka-sync-fixture differs from the registered repository namespace other; run 'eka sync --override' to align the repository identity to eka-sync-fixture"
		if err.Error() != want {
			t.Errorf("refusal = %q, want the pinned byte-exact string %q", err.Error(), want)
		}
	})
}

// TestSyncContentNamespaceMultiRefused (ADR-020): content spanning
// MULTIPLE distinct namespaces is refused WITHOUT override — a
// repository is one platform, consolidate the content first.
func TestSyncContentNamespaceMultiRefused(t *testing.T) {
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	// A second conformant artifact with a DIFFERENT namespace: copy the
	// ADR and change namespace + id (a duplicate identity would fail
	// the docs gate before reconciliation). The depends-on is dropped:
	// `sto:login-email` resolves within the artifact's own namespace,
	// which changed — an unresolved reference would fail the docs gate
	// (R5) before reconciliation.
	adr, err := os.ReadFile(filepath.Join(repoDir, "docs", "decisions", "adr-001-runtime.md"))
	if err != nil {
		t.Fatal(err)
	}
	second := strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(string(adr),
		"namespace: eka-sync-fixture", "namespace: eka-sync-fixture-b"),
		"id: 001-runtime", "id: 002-extra"),
		"depends-on:\n  - sto:login-email", "depends-on: []")
	if err := os.WriteFile(filepath.Join(repoDir, "docs", "decisions", "adr-002-extra.md"), []byte(second), 0o644); err != nil {
		t.Fatal(err)
	}
	writeEKAFile(t, repoDir, "eka-sync-fixture", "eka-sync-fixture", "other")

	// Override does NOT bypass the multi-ns refusal.
	_, err = Run(w, repoDir, Options{Pull: true, Push: true, Override: true})
	if err == nil {
		t.Fatal("multi-ns content must refuse the sync even with Override")
	}
	want := "sync refused: the repository content spans multiple namespaces (eka-sync-fixture, eka-sync-fixture-b); a repository is one platform — consolidate the content"
	if err.Error() != want {
		t.Errorf("refusal = %q, want the pinned byte-exact string %q", err.Error(), want)
	}
	// The refusal leaves the identity untouched.
	parsed, err := metadata.Parse(mustRead(t, filepath.Join(repoDir, "eka.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Namespace != "other" {
		t.Errorf("eka.yaml namespace after refusal = %q, want other (no rewrite)", parsed.Namespace)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "exchange", "snapshots")); !os.IsNotExist(err) {
		t.Error("a refused sync must not write a snapshot")
	}
	// The refusal leaves the STORE untouched (ADR-020 D3: detection
	// runs before any store mutation — no registration row, no seeded
	// units).
	if _, found, err := w.FindRepo(repoDir); err != nil || found {
		t.Errorf("a refused sync must not register the repository (found = %v, err = %v)", found, err)
	}
	units, err := w.Store().Units("eka-sync-fixture", "eka-sync-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 0 {
		t.Errorf("a refused sync must not seed units, got %d", len(units))
	}
}

// mustRead reads a file, failing the test on error.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestPullKeepsNewerInstanceFromStaleWorktree: the forward-only
// reference guard at the sync layer — a sync run against a stale
// worktree (docs content older than what the store resolves) must not
// regress the reference, and the pull report surfaces the kept-newer
// identities. This is the observed silent-regression scenario: a
// sub-agent runs `eka sync` in a worktree whose snapshot/docs predate
// a transition the primary already published to the store.
func TestPullKeepsNewerInstanceFromStaleWorktree(t *testing.T) {
	w := newWorkspace(t)

	// Two trees with the SAME repository identity (ADR-017): the
	// primary (synced first) and a stale worktree carrying the old
	// docs content.
	primary := copyFixture(t)
	stale := copyFixture(t)

	// First sync from the primary: register + seed + snapshot.
	if _, err := Run(w, primary, both()); err != nil {
		t.Fatalf("first sync: %v", err)
	}

	// The store advances the adr line with a transition-like publish:
	// SAME instance version, NEW payload (change-log appended). The
	// stale worktree still carries the ORIGINAL payload.
	form := "eka-sync-fixture/adr:001-runtime:1"
	repo, ok, err := w.FindRepo(primary)
	if err != nil || !ok {
		t.Fatalf("FindRepo: %v, %v", ok, err)
	}
	next := &exchange.Unit{
		Identity: exchange.Identity{Namespace: "eka-sync-fixture", Type: "adr", ID: "001-runtime", InstanceVersion: 1},
		Revision: 1,
		StateVector: exchange.StateVector{
			ContentState:   "accepted",
			ExistenceState: "active",
		},
		ChangeLog: []exchange.ChangeLogEntry{
			{Date: "2026-08-05", Domain: "existence-state", From: "-", To: "active", By: conformance.User("Engineering Architecture")},
			{Date: "2026-08-05", Domain: "content-state", From: "proposed", To: "accepted", By: conformance.User("Engineering Architecture")},
			{Date: "2026-08-06", Domain: "content-state", From: "accepted", To: "approved", By: conformance.User("test")},
		},
		Relationships: []exchange.Relationship{
			{Type: "depends-on", Target: "eka-sync-fixture/sto:login-email:1"},
		},
		Classification: exchange.Classification{
			Dimension: "decisions",
			Domain:    "Architecture",
		},
		Phase:          "wave-1",
		Content:        exchange.ContentRef{Representation: "eka/structured-text/1", File: "content"},
		ContentPayload: []byte("content v2"),
	}
	next.CanonicalIdentityForm = next.Identity.CanonicalForm()
	nextJSON, err := exchange.MarshalUnit(next)
	if err != nil {
		t.Fatal(err)
	}
	hNew, _, err := w.Store().PutUnit(nextJSON, next.ContentPayload, store.Ref{
		Form:            next.CanonicalIdentityForm,
		ProjectID:       repo.ProjectID,
		SourceRepo:      repo.Name,
		Namespace:       next.Identity.Namespace,
		Type:            next.Identity.Type,
		ID:              next.Identity.ID,
		InstanceVersion: next.Identity.InstanceVersion,
		Revision:        next.Revision,
		Dimension:       next.Classification.Dimension,
		Domain:          next.Classification.Domain,
		Phase:           next.Phase,
		UpdatedAt:       "2026-08-06T00:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Sync from the stale worktree: docs-mode pull re-seeds the
	// ORIGINAL payload (an ancestor of the referenced payload); the
	// guard must keep the reference at the newer instance and report
	// the kept identity.
	report, err := Run(w, stale, both())
	if err != nil {
		t.Fatalf("stale worktree sync: %v", err)
	}
	kept := false
	for _, k := range report.KeptNewer {
		if strings.Contains(k, form) {
			kept = true
		}
	}
	if !kept {
		t.Errorf("KeptNewer = %v, want it to contain %s", report.KeptNewer, form)
	}
	got, ok, err := w.Store().Ref(form)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("reference missing after the stale sync")
	}
	if got.ObjectHash != hNew {
		t.Errorf("reference must stay at the newer payload %s, regressed to %s", hNew, got.ObjectHash)
	}
}
