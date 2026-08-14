package conformance

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the validation engine entry point:
//
//	report, err := conformance.Validate(root)
//
// The engine walks the <root>/docs knowledge tree, classifies every
// authoring file (.md legacy and .json v2.0-native), and runs Rules
// R1-R12 over the collected artifacts. When <root>/docs does not exist,
// validation is SKIPPED — the report is a clean PASS carrying the
// deterministic skip note (docs-in-repo is legacy v1 authoring, not an
// obligation). It never imports anything from cmd/ and is fully
// reusable by future tooling.
//
// Scan policy (documented interpretation):
//   - The scan scope is the <root>/docs knowledge tree only. Markdown
//     outside docs/ (README.md, skeleton docs, convention docs) is never
//     examined: the conformance gate certifies the knowledge tree, not
//     the whole repository (ADR-018 Decision 2).
//   - Every .md file under docs/ is examined (v1.1 behavior, unchanged);
//     .json files are examined only when their name matches the v2.0
//     authoring naming contract `<type-token>-<id>.json` — foreign/config
//     JSON (composer.json, package.json, lock files, RSF package entries)
//     is never scanned.
//   - Convention documents (no frontmatter, or frontmatter without both
//     `type` and `id`) are counted but skipped.
//   - Directories named "testdata" and directories starting with "." (e.g.
//     .git) are not descended into: they hold test fixtures and VCS
//     metadata, not repository knowledge content. Without this, Go test
//     fixtures under conformance/testdata would be validated as if they were
//     part of the knowledge base.
//   - Symlinks are not followed (filepath.WalkDir behavior).
//   - An unreadable authoring file aborts the run with an error (a scan that
//     cannot see every file cannot certify compliance).
func Validate(root string) (*Report, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("cannot access scan root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root is not a directory: %s", root)
	}

	report := &Report{Root: filepath.Clean(root)}

	// Conformance scope (ADR-018 Decision 2): the gate scans the docs/
	// knowledge tree only. Without docs/, validation is skipped — never
	// a whole-root sweep — with the deterministic skip note; a skipped
	// report is a clean PASS.
	if !docsTreeExists(root) {
		report.Skipped = skipNoDocsTree
		return report, nil
	}

	e := &engine{report: report}

	paths, err := collectAuthoringPaths(root)
	if err != nil {
		return nil, err
	}
	report.FilesScanned = len(paths)

	// Parse phase: classify every file. Parse failures become R0 results.
	for _, path := range paths {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("cannot read %s: %w", rel, err)
		}
		artifact, results := analyzeFile(rel, path, data)
		report.Results = append(report.Results, results...)
		if artifact != nil {
			e.artifacts = append(e.artifacts, artifact)
		}
	}
	report.Artifacts = len(e.artifacts)

	// Identity index for reference resolution and the R9 supersession
	// check.
	e.buildIndex()

	// Rules. R0 results were collected during parsing; R1 runs over the
	// whole set; R2-R12 run per artifact.
	e.rule1()
	for _, a := range e.artifacts {
		e.rule2(a)
		e.rule3(a)
		e.rule4(a)
		e.rule5(a)
		e.rule6(a)
		e.rule7(a)
		e.rule8(a)
		e.rule9(a)
		e.rule10(a)
		e.rule11(a)
		e.rule12(a)
	}
	// The graph pass (R13, ADR-019): the first cross-file validation —
	// the cmt- note contract, discusses resolution and the work-item
	// transition gates. Runs after the per-file pass, before the
	// package is accepted.
	report.Results = append(report.Results, ValidateGraph(e.artifacts)...)

	return report, nil
}

// skipNoDocsTree is the deterministic skip reason pinned by ADR-018
// Decision 2: when the <root>/docs knowledge tree is absent, validation
// is skipped — not refused — because docs-in-repo is legacy v1
// authoring, not an obligation. The exact string is part of the
// documented output contract.
const skipNoDocsTree = "no docs/ knowledge tree — nothing to validate (docs-in-repo is legacy authoring — EKA v2 keeps knowledge in the workspace)"

// docsTreeExists reports whether root carries a docs/ knowledge tree: a
// directory named "docs" directly under root. A file named "docs" is not
// a knowledge tree.
func docsTreeExists(root string) bool {
	info, err := os.Stat(filepath.Join(root, "docs"))
	return err == nil && info.IsDir()
}

// collectAuthoringPaths walks root's docs/ knowledge tree and returns
// the absolute paths of every authoring file under it, applying the
// shared scan policy (documented in the package doc):
//   - the scan scope is <root>/docs only; when the docs tree does not
//     exist (or "docs" is a file), the collection is EMPTY and the scan
//     is skipped — the root is never walked wholesale anymore (ADR-018
//     Decision 2; the legacy whole-tree sweep is removed);
//   - .md files are always collected (legacy behavior: every .md is
//     examined and classified; convention documents are skipped by the
//     frontmatter check);
//   - .json files are collected only when their name matches the v2.0
//     authoring naming contract (<type-token>-<id>.json,
//     conformance.IsJSONAuthoringName) — composer.json, package.json,
//     lock files and RSF package entries are configuration/foreign
//     files and are never scanned;
//   - directories named "testdata" and directories starting with "." (e.g.
//     .git) are not descended into;
//   - symlinks are not followed (filepath.WalkDir behavior);
//   - a walk error (e.g. an unreadable directory) aborts the scan.
//
// Both Validate and Scan use it, so the two entry points share one scan
// policy by construction.
func collectAuthoringPaths(root string) ([]string, error) {
	if !docsTreeExists(root) {
		return nil, nil
	}
	docs := filepath.Join(root, "docs")
	var paths []string
	err := filepath.WalkDir(docs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != docs {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "testdata" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		switch {
		case strings.HasSuffix(d.Name(), ".md"):
			paths = append(paths, path)
		case strings.HasSuffix(d.Name(), ".json") && IsJSONAuthoringName(d.Name()):
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan failed: %w", err)
	}
	return paths, nil
}

// engine carries the state shared by all rule checks.
type engine struct {
	report    *Report
	artifacts []*Artifact
	// byLine indexes artifacts by identity line (namespace, type, id);
	// each bucket holds all instances sorted by instance-version.
	byLine map[string][]*Artifact
	// skipLocation skips the location-shaped rules — R2 (filename
	// consistency) and R6 (dimension == folder) — for CKO-level
	// validation (ValidateCKO), where the artifact has no file path.
	// Every other rule operates on artifact content and stays active.
	skipLocation bool
	// ckoResolve is the CKO-mode relationship resolution callback:
	// reports whether a parsed reference resolves in the runtime. When
	// nil (the repository Validate path), Rule 5 resolves against the
	// engine's own artifact index.
	ckoResolve func(ref Reference) bool
	// draftRef is the CKO-mode draft-target callback: reports whether
	// an unresolved reference target exists as a draft (an unpublished
	// authoring object) of the same project. Rule 5 consults it after
	// resolution fails: a line-level reference whose target is a draft
	// is an allowed draft-to-draft authoring reference and produces no
	// finding. Nil = no draft knowledge (every unresolved reference is
	// reported).
	draftRef func(ref Reference) bool
}

// identityKey builds the identity line key for the index.
func identityKey(ns, typeToken, id string) string {
	return ns + "\x00" + typeToken + "\x00" + id
}

func (e *engine) buildIndex() {
	e.byLine = make(map[string][]*Artifact)
	for _, a := range e.artifacts {
		key := identityKey(a.Namespace, a.Type, a.ID)
		e.byLine[key] = append(e.byLine[key], a)
	}
	for _, bucket := range e.byLine {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].InstanceVersion < bucket[j].InstanceVersion
		})
	}
}

// add appends a result for an artifact.
func (e *engine) add(a *Artifact, rule string, sev Severity, format string, args ...any) {
	e.report.Results = append(e.report.Results, Result{
		File:     a.RelPath,
		Rule:     rule,
		Severity: sev,
		Message:  fmt.Sprintf(format, args...),
	})
}

// addFile appends a result not tied to a parsed artifact.
func (e *engine) addFile(rel, rule string, sev Severity, format string, args ...any) {
	e.report.Results = append(e.report.Results, Result{
		File:     rel,
		Rule:     rule,
		Severity: sev,
		Message:  fmt.Sprintf(format, args...),
	})
}
