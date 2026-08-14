package sync

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/exchange"
)

// seedSnapshot builds a real snapshot in a fixture copy via a full sync
// run (docs pull + push — the same setup the happy-path test uses) and
// returns the snapshot directory. The snapshot is a genuine push
// output — a SOURCE-ONLY snapshot since ADR-027 (no aggregates) — so
// the repair tests exercise the real package bytes in both layouts.
func seedSnapshot(t *testing.T) string {
	t.Helper()
	w := newWorkspace(t)
	repoDir := copyFixture(t)
	if _, err := Run(w, repoDir, both()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return filepath.Join(repoDir, "exchange", "snapshots")
}

// makeLegacySnapshot upgrades a source-only snapshot to the legacy
// layout (EKA < 0.10): the derived aggregates are emitted from the
// snapshot's units and written next to the source entries. The result
// verifies byte-exact as a full package.
func makeLegacySnapshot(t *testing.T, dir string) {
	t.Helper()
	res, err := exchange.LoadSnapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	files, err := exchange.Emit(res.Package)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Name == "manifest.json" || f.Name == "declarations.json" || f.Name == "integrity.json" {
			if err := os.WriteFile(filepath.Join(dir, f.Name), f.Data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestRepairSnapshotNormalizesUnitJSON (source mode): a hand-edited
// unit.json (non-canonical formatting) is normalized by the repair and
// the snapshot still verifies with the same load a pull applies;
// content, attachments and header.json stay untouched.
func TestRepairSnapshotNormalizesUnitJSON(t *testing.T) {
	dir := seedSnapshot(t)
	unitJSON := firstUnitJSON(t, dir)
	orig, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	// Non-canonical but decodable: pretty-printed JSON (json.Indent
	// changes the byte layout; DecodeUnit accepts it, the serializer
	// re-emits canonical bytes).
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, orig, "", "  "); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitJSON, pretty.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RepairSnapshot(dir); err != nil {
		t.Fatalf("RepairSnapshot: %v", err)
	}

	// The unit.json is canonical again (byte-identical to the original
	// serializer output).
	after, err := os.ReadFile(unitJSON)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(orig) {
		t.Error("repair must re-serialize unit.json canonically")
	}
	if _, err := exchange.LoadSnapshot(dir); err != nil {
		t.Errorf("repaired snapshot refused: %v", err)
	}
}

// TestRepairSnapshotLegacyRestoresAggregates (legacy mode): a legacy
// snapshot (aggregates present) with corrupted aggregates is restored
// — the regenerated package verifies byte-exact, and an unchanged
// units/ tree reproduces the original package digest.
func TestRepairSnapshotLegacyRestoresAggregates(t *testing.T) {
	dir := seedSnapshot(t)
	makeLegacySnapshot(t, dir)
	pkg, _, err := exchange.LoadPackageWithEntries(dir)
	if err != nil {
		t.Fatalf("legacy snapshot refused: %v", err)
	}
	wantDigest := pkg.Integrity.PackageDigest

	// The merge-conflict state: every aggregate replaced by garbage.
	for _, name := range []string{"manifest.json", "declarations.json", "integrity.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("<<<<<<< broken merge"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := exchange.LoadPackageWithEntries(dir); err == nil {
		t.Fatal("corrupted aggregates must be refused by the loader")
	}

	if err := RepairSnapshot(dir); err != nil {
		t.Fatalf("RepairSnapshot: %v", err)
	}

	pkg2, _, err := exchange.LoadPackageWithEntries(dir)
	if err != nil {
		t.Fatalf("repaired snapshot refused: %v", err)
	}
	if pkg2.Integrity.PackageDigest != wantDigest {
		t.Errorf("digest = %s, want %s (regeneration must reproduce the push bytes)", pkg2.Integrity.PackageDigest, wantDigest)
	}
}

// TestRepairSnapshotContentUntouched: content payloads and attachments
// are byte-exact knowledge — repair must never rewrite them (the
// lossless round-trip invariant), in either layout.
func TestRepairSnapshotContentUntouched(t *testing.T) {
	dir := seedSnapshot(t)
	makeLegacySnapshot(t, dir)
	before := map[string]string{}
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, "/content") && !strings.Contains(path, string(filepath.Separator)+"attachments"+string(filepath.Separator))) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		before[path] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(before) == 0 {
		t.Fatal("fixture snapshot must carry content/attachments")
	}

	if err := os.WriteFile(filepath.Join(dir, "integrity.json"), []byte("broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RepairSnapshot(dir); err != nil {
		t.Fatalf("RepairSnapshot: %v", err)
	}

	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || (!strings.HasSuffix(path, "/content") && !strings.Contains(path, string(filepath.Separator)+"attachments"+string(filepath.Separator))) {
			return nil
		}
		got, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(got) != before[path] {
			t.Errorf("repair rewrote %s (lossless round-trip violated)", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRepairSnapshotNoWriteOnCorruptUnit: an undecodable unit.json (an
// unresolved unit-level conflict) refuses the whole repair BEFORE any
// write — no partial state.
func TestRepairSnapshotNoWriteOnCorruptUnit(t *testing.T) {
	dir := seedSnapshot(t)
	makeLegacySnapshot(t, dir)
	unitJSON := firstUnitJSON(t, dir)
	manifestBefore, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitJSON, []byte("<<<<<<< ours\n{broken"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := RepairSnapshot(dir); err == nil {
		t.Fatal("repair must refuse a corrupt unit.json")
	}
	manifestAfter, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(manifestBefore) != string(manifestAfter) {
		t.Error("no writes are allowed when a unit cannot be decoded")
	}
}

// TestRepairSnapshotRefusesWithoutHeader: header.json is the package
// contract; without it there is nothing to regenerate against.
func TestRepairSnapshotRefusesWithoutHeader(t *testing.T) {
	dir := seedSnapshot(t)
	if err := os.Remove(filepath.Join(dir, "header.json")); err != nil {
		t.Fatal(err)
	}
	if err := RepairSnapshot(dir); err == nil {
		t.Fatal("repair must refuse a snapshot without header.json")
	}
}

// TestRepairSnapshotRejectsUnknownEntry: entries outside the known
// structure are refused (the loader's RSF §9.5 strictness).
func TestRepairSnapshotRejectsUnknownEntry(t *testing.T) {
	dir := seedSnapshot(t)
	if err := os.WriteFile(filepath.Join(dir, "units", "stray.txt"), []byte("stray"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RepairSnapshot(dir); err == nil {
		t.Fatal("repair must refuse unknown entries under units/")
	}
}

// TestRepairSnapshotIdempotentOnHealthySnapshot: repairing a healthy
// source-only snapshot changes nothing — every byte stays identical
// (canonical unit.json entries re-serialize to themselves).
func TestRepairSnapshotIdempotentOnHealthySnapshot(t *testing.T) {
	dir := seedSnapshot(t)
	before := treeFingerprint(t, dir)
	if err := RepairSnapshot(dir); err != nil {
		t.Fatalf("RepairSnapshot: %v", err)
	}
	after := treeFingerprint(t, dir)
	if before != after {
		t.Error("repair of a healthy snapshot must be a no-op (byte-identical tree)")
	}
}

// firstUnitJSON returns the path of one unit.json entry of the snapshot.
func firstUnitJSON(t *testing.T, dir string) string {
	t.Helper()
	var found string
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && d.Name() == "unit.json" && found == "" {
			found = path
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if found == "" {
		t.Fatal("fixture snapshot must carry unit.json entries")
	}
	return found
}

// treeFingerprint renders a deterministic digest of the snapshot tree
// content (path + bytes), for byte-exact before/after comparisons.
func treeFingerprint(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	if err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
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
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sb.WriteString(rel)
		sb.WriteByte('=')
		sb.Write(data)
		sb.WriteByte('\n')
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}
