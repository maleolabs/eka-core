package conformance

import (
	"testing"
)

// Tests of the JSON authoring scan gateway: a .json file is examined
// only when its name matches the v2.0 authoring naming contract
// (<type-token>-<id>.json). Foreign/config JSON — composer.json,
// package.json, tsconfig.json, lock files, RSF package entries — is
// never scanned, so a repository holding such files validates clean.

// TestForeignJSONFilesNotScanned: a repo with composer.json (which
// carries a `type` field — the exact false-positive class),
// package.json, tsconfig.json and a lock file validates clean; only
// the naming-contract files are scanned.
func TestForeignJSONFilesNotScanned(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"composer.json": `{
			"name": "acme/app",
			"type": "library",
			"require": {"php": ">=8.2"}
		}`,
		"package.json": `{
			"name": "acme-app",
			"version": "1.0.0",
			"type": "module"
		}`,
		"tsconfig.json": `{
			"compilerOptions": {"strict": true}
		}`,
		"composer.lock": `{"packages": []}`,
		// The naming-contract artifacts that must be scanned.
		"docs/decisions/adr-001-config.json": `{
			"namespace": "acme", "type": "adr", "id": "001-config", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-09", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-09", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co", "alternativesConsidered": "A"}
		}`,
		"docs/operating/work-items/stories/sto-config.json": `{
			"namespace": "acme", "type": "sto", "id": "config", "instanceVersion": 1, "revision": 1,
			"state": {"executionState": "planned", "existenceState": "active"},
			"changeLog": [
				{"date": "2026-08-09", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-09", "domain": "executionState", "from": "-", "to": "planned", "by": "T"}
			],
			"content": {"description": "d", "acceptanceCriteria": "ac"}
		}`,
		"docs/README.md": "# repo",
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("foreign JSON must never produce findings:\n%s", dumpResults(report))
	}
	if !report.Pass() {
		t.Error("repo with config JSON must pass")
	}
	// Exactly the two naming-contract artifacts are scanned (the .md
	// README counts too): composer.json/package.json/tsconfig.json/
	// composer.lock are invisible to the scan.
	if report.FilesScanned != 3 {
		t.Errorf("files scanned = %d, want 3 (README.md + 2 authoring files)", report.FilesScanned)
	}
	if report.Artifacts != 2 {
		t.Errorf("artifacts = %d, want 2", report.Artifacts)
	}
}

// TestJSONAuthoringNameContract: the gateway accepts exactly the
// <type-token>-<id>.json shape with a known token.
func TestJSONAuthoringNameContract(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"adr-001-login-serialization.json", true},
		{"sto-my-item.json", true},
		{"plan-roadmap-2026-v1.json", true},
		{"tkt-sto-login-email.json", true},
		{"composer.json", false},
		{"package.json", false},
		{"tsconfig.json", false},
		{"composer.lock", false},
		{"unit.json", false},     // RSF package entry: no token split
		{"manifest.json", false}, // RSF package entry
		{"adr.json", false},      // no id segment
		{"mydoc.json", false},    // unknown token
		{"README.json", false},   // convention doc outside the contract
		{"adr-001.json", true},
	}
	for _, tc := range cases {
		if got := IsJSONAuthoringName(tc.name); got != tc.want {
			t.Errorf("IsJSONAuthoringName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestMisnamedJSONArtifactNotScanned: a JSON artifact whose file name
// violates the naming contract is invisible to the repository scan
// (the name is the entry contract, spec-standard-v2 §3.1); the same
// content under a contract name validates. Documented behavior, pinned
// by this test.
func TestMisnamedJSONArtifactNotScanned(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"docs/decisions/mydoc.json": `{
			"namespace": "acme", "type": "adr", "id": "001-x", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-09", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-09", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co", "alternativesConsidered": "A"}
		}`,
	})
	report, err := Validate(root)
	if err != nil {
		t.Fatal(err)
	}
	if report.Artifacts != 0 {
		t.Errorf("artifacts = %d, want 0 (the name is the entry contract)", report.Artifacts)
	}
	if report.ErrorCount() != 0 {
		t.Errorf("a non-contract file must never produce findings:\n%s", dumpResults(report))
	}
}

// TestScanSkipsForeignJSON: Scan (the classification API) shares the
// gateway — config JSON never reaches the classifier.
func TestScanSkipsForeignJSON(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"package.json": `{"name": "x", "type": "module"}`,
		"docs/decisions/adr-001-scan.json": `{
			"namespace": "acme", "type": "adr", "id": "001-scan", "instanceVersion": 1, "revision": 1,
			"state": {"contentState": "accepted", "existenceState": "active"},
			"dimension": "decisions",
			"changeLog": [
				{"date": "2026-08-09", "domain": "existenceState", "from": "-", "to": "active", "by": "T"},
				{"date": "2026-08-09", "domain": "contentState", "from": "proposed", "to": "accepted", "by": "T"}
			],
			"content": {"context": "C", "decision": "D", "consequences": "Co", "alternativesConsidered": "A"}
		}`,
	})
	artifacts, err := Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 || artifacts[0].ID != "001-scan" {
		t.Errorf("Scan = %+v, want exactly the naming-contract artifact", artifacts)
	}
}
