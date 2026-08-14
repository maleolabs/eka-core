// Package sync implements the EKA Knowledge Runtime synchronization
// engine (milestone EKA v0.2.0): the bidirectional transport between a
// registered repository and the EKA workspace canonical store.
//
// Model (documented):
//
//   - The workspace is canonical: immutable Engineering Knowledge
//     Objects (content-addressed payloads), their mutable references,
//     attachments and the sync log live in the workspace database
//     (eka.db). Relationships and change logs are serialized inside
//     the immutable payload (unit.json), never stored separately.
//   - The transport between a repository and the workspace is the
//     snapshot directory <repo>/exchange/snapshots: the SOURCE entries of
//     an RSF package in directory layout (header.json, units/,
//     attachments/ — the derived aggregates are not committed since
//     ADR-027), verified on every read (exchange.LoadSnapshot) and
//     emitted deterministically (exchange.EmitSource).
//   - Pull (snapshot mode): the snapshot is verified (exchange.LoadSnapshot
//     — source-only snapshots structurally and per-unit, legacy packages
//     carrying the aggregates byte-exact) and its units are seeded as
//     immutable payloads (store.PutUnit: unit.json entry bytes verbatim +
//     content, content-addressed), attributed to the repository via the
//     reference (project_id + source_repo).
//     Idempotent: an unchanged package digest skips the work; re-seeding
//     the same package is a no-op. Deletions are never applied.
//     Re-seeds are guarded FORWARD-ONLY: a pull whose payload is an
//     older instance of a referenced line (an ancestor of the referenced
//     payload in the store's prev_hash lineage) keeps the reference at
//     the newer instance and reports the identity in the pull result
//     (KeptNewer) — a stale snapshot can never silently regress a newer
//     knowledge state (e.g. an item the workspace advanced to
//     in-progress resolving back to todo).
//   - Pull (docs mode): the repository's docs tree is compiled through
//     the Knowledge Compiler (the compile package: the authoring
//     conformance gate, then the package assembled exactly as
//     `eka export` would via exchange.RepositoryPackage), then seeded
//     the same way with unit.json bytes serialized via
//     exchange.MarshalUnit. This is the migration path for repositories
//     without a snapshot, and the --from-docs re-seed path.
//   - Push: the repository's canonical units in the workspace store are
//     read back (store.Units), assembled into an RSF package
//     (namespace resolution: the repository identity metadata
//     eka.yaml when present — the registered repository namespace,
//     frozen after the first sync; legacy repositories keep the
//     existing-snapshot header, else most common namespace, else
//     error) and emitted into <repo>/exchange/snapshots atomically
//     (write to .snapshots-tmp, then swap).
//
// Repository context (ADR-018): an EKA repository is a directory
// tree carrying eka.yaml. The walk-up locates it; a directory without
// it is NOT an EKA repository — sync refuses deterministically (run
// 'eka init' first). Auto-registration (ADR-017): a repository whose
// tree carries eka.yaml is registered with the identity from the file
// — project, name and namespace — never the path basename; the
// identity pair (project, namespace) is immutable after the first
// sync. The legacy basename auto-registration branch is REMOVED.
//
// The sync log (store.RecordSync) records every pull/push run and
// backs the idempotent-pull check and `eka status`.
//
// Error classes: validation failures (docs gate) and integrity
// failures (corrupt snapshot) are returned as typed errors mapped to
// exit code 1 by the CLI; workspace/registry/usage failures map to
// exit code 2.
package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/workspace"
)

// Options configures one sync run. Pull and Push both default to true
// (`eka sync`); a pull-only run sets Push=false, a push-only run sets
// Pull=false.
type Options struct {
	Pull bool
	Push bool
	// FromDocs seeds the canonical store from the repository's docs
	// tree (migration/re-seed) instead of the snapshot directory.
	FromDocs bool
	// Override aligns the repository identity to the CONTENT
	// namespace when they differ (ADR-020 Decision 3): the sanctioned
	// identity-change path — eka.yaml namespace rewritten and
	// repos.namespace updated, then the identity freezes again. When
	// false, a single-ns mismatch is refused deterministically.
	Override bool
	// Confirm is the interactive confirmation used when Override is
	// not set and the repository content resolves to exactly one
	// namespace differing from the registered one. It receives the
	// prompt, the selectable options and the default index, and
	// returns the chosen index. Nil never prompts (a mismatch is
	// refused); the caller wires a TTY arrow-selected prompt (the
	// bootstrap.ConfirmOverwrite precedent). Injectable for tests.
	Confirm func(prompt string, options []string, defaultIdx int) (int, error)
}

// Report is the deterministic outcome of one sync run.
type Report struct {
	// Workspace is the workspace root.
	Workspace string
	// Project and Repo identify the synced repository.
	Project string
	Repo    string
	// PullSource is "snapshot" or "docs" ("" when no pull ran).
	PullSource string
	// PulledUnits/PulledAttachments count the pull work.
	PulledUnits       int
	PulledAttachments int
	// PushedUnits/PushedAttachments count the push work (0 in a
	// no-op push with no stored objects).
	PushedUnits       int
	PushedAttachments int
	// SnapshotLabel is the pushed package label ("" when nothing was
	// pushed); SnapshotDigest is the package digest ("" when neither
	// pull nor push produced a digest).
	SnapshotLabel  string
	SnapshotDigest string
	// NewRepo reports whether the repository was registered by this
	// run.
	NewRepo bool
	// Unchanged reports an idempotent pull (snapshot digest already
	// current — no pull work done).
	Unchanged bool
	// PushChanged reports that the push rewrote the snapshot with a
	// DIFFERENT digest than the one it replaced: the canonical store
	// and the repository snapshot were out of sync (a tampered store
	// is the typical cause). A clean re-push of identical state
	// reports false.
	PushChanged bool
	// Overwrites lists the identities this run replaced from another
	// repository (deterministic order, empty when none).
	Overwrites []string
	// KeptNewer lists the identities whose reference was NOT re-pointed
	// by the pull because the canonical store already resolves a NEWER
	// instance version (forward-only reference guard — the pull
	// re-seeded an older instance; deterministic order, empty when
	// none).
	KeptNewer []string
	// Warnings are informational notes, in deterministic order.
	Warnings []string
}

// namespaceImmutableError is the deterministic refusal when the
// eka.yaml namespace differs from the registered repository namespace:
// the identity pair (project, namespace) is frozen after the first
// sync, because the namespace is part of every unit identity — a silent
// change would detach all history (ADR-017 D5).
func namespaceImmutableError(fileNS, repoNS string) error {
	return fmt.Errorf(
		"sync refused: eka.yaml namespace %s differs from the registered repository namespace %s; the repository identity (project, namespace) is frozen after the first sync — align eka.yaml or re-register the repository explicitly",
		fileNS, repoNS)
}

// distinctNamespaces returns the distinct non-empty namespaces of a
// namespace list, sorted lexicographically (deterministic order for
// the multi-ns refusal list). Validated content always carries one
// namespace; empty values are skipped defensively.
func distinctNamespaces(nsList []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ns := range nsList {
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// contentNamespaces returns the distinct namespaces of the
// repository's units in the canonical store (store.Units: the decoded
// immutable payloads), sorted lexicographically. The store units are
// the CONTENT authority (ADR-020 Decision 3, P3: identity lives in the
// file body — the units are seeded from the file bodies by the pull).
// It is the store fallback of the reconciliation detection (see
// sourceContentNamespaces).
func contentNamespaces(ws *workspace.Workspace, projectID, repoName string) ([]string, error) {
	units, err := ws.Store().Units(projectID, repoName)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot read repository units: %w", err)
	}
	var nsList []string
	for _, u := range units {
		nsList = append(nsList, u.Identity.Namespace)
	}
	return distinctNamespaces(nsList), nil
}

// sourceContentNamespaces returns the distinct namespaces of the
// repository's CONTENT, read from the source BEFORE any registration,
// pull or store write (ADR-020 Decision 3: detection runs before any
// store mutation — a refused run writes nothing). Source precedence:
//
//  1. Snapshot mode (an exchange/snapshots directory exists at root AND
//     opts.FromDocs is false): the snapshot is verified with the exact
//     entry point the pull uses (exchange.LoadSnapshot — source-only
//     snapshots structurally and per-unit, legacy packages byte-exact)
//     and the namespaces are read from the loaded units' identities. A
//     corrupt snapshot is an error — the same refusal the pull
//     produces (integrity failure class, exit 1).
//  2. Docs mode (--from-docs, or no snapshot): conformance.Scan(root)
//     — the read-only docs-scoped scan — and the namespaces are read
//     from the scanned artifacts. A docs tree with zero artifacts
//     scans EMPTY (no error).
//  3. Store fallback (push-only preservation): when the source yields
//     NO content — an empty docs scan, an empty package — but the
//     canonical store already has units for the repository identity
//     (m.Project/m.Name, the ADR-017 identity), the stored units are
//     used: a push-only run on an already-seeded store keeps
//     reconciling against the seeded content. Both empty → no check.
func sourceContentNamespaces(w *workspace.Workspace, root string, m metadata.Metadata, opts Options) ([]string, error) {
	snapshotDir := filepath.Join(root, "exchange", "snapshots")
	if !opts.FromDocs {
		if info, err := os.Stat(snapshotDir); err == nil && info.IsDir() {
			res, err := exchange.LoadSnapshot(snapshotDir)
			if err != nil {
				// Integrity failure class: identical refusal to the
				// pull path (the CLI maps the wrapped PackageError to
				// exit 1).
				return nil, fmt.Errorf("sync pull failed: snapshot package refused: %w", err)
			}
			var nsList []string
			for _, u := range res.Package.Units {
				nsList = append(nsList, u.Identity.Namespace)
			}
			if list := distinctNamespaces(nsList); len(list) > 0 {
				return list, nil
			}
		}
	}
	artifacts, err := conformance.Scan(root)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot scan the repository content: %w", err)
	}
	var nsList []string
	for _, a := range artifacts {
		nsList = append(nsList, a.Namespace)
	}
	if list := distinctNamespaces(nsList); len(list) > 0 {
		return list, nil
	}
	return contentNamespaces(w, m.Project, m.Name)
}

// namespaceMismatchError is the deterministic single-ns refusal
// (ADR-020 §6A.3, byte-pinned): the repository content resolves to
// exactly ONE distinct namespace differing from the registered one —
// the identity is aligned to the content via --override, never
// silently.
func namespaceMismatchError(contentNS, repoNS string) error {
	return fmt.Errorf(
		"sync refused: the repository content namespace %s differs from the registered repository namespace %s; run 'eka sync --override' to align the repository identity to %s",
		contentNS, repoNS, contentNS)
}

// namespaceMultiError is the deterministic multi-ns refusal
// (ADR-020 §6A.3, byte-pinned): the repository content spans multiple
// distinct namespaces — a repository is one platform, consolidate the
// content first. Never overridable.
func namespaceMultiError(nsList []string) error {
	return fmt.Errorf(
		"sync refused: the repository content spans multiple namespaces (%s); a repository is one platform — consolidate the content",
		strings.Join(nsList, ", "))
}

// rewriteEKANamespace rewrites eka.yaml at root with the content
// namespace (canonical Marshal bytes; project/name untouched — ADR-020
// §6A.3). The file is re-read + re-parsed at write time: the file is
// authoritative; a file that stopped parsing must refuse before being
// replaced. It returns the deterministic aligned note (appended to the
// report warnings).
//
// It performs NO store write: detection runs before any store mutation
// (ADR-020 D3) and the registry row may not exist yet — the resolution
// step records the aligned namespace afterwards, either on the found
// row (SetRepoNamespace, replacing the immutability check) or in the
// registration INSERT.
func rewriteEKANamespace(root, oldNS, contentNS string) (string, error) {
	data, err := os.ReadFile(filepath.Join(root, "eka.yaml"))
	if err != nil {
		return "", fmt.Errorf("sync: cannot read eka.yaml for the namespace alignment: %w", err)
	}
	parsed, err := metadata.Parse(data)
	if err != nil {
		return "", fmt.Errorf("sync: cannot parse eka.yaml for the namespace alignment: %w", err)
	}
	aligned := metadata.Metadata{Version: parsed.Version, Project: parsed.Project, Name: parsed.Name, Namespace: contentNS}
	if err := os.WriteFile(filepath.Join(root, "eka.yaml"), aligned.Marshal(), 0o644); err != nil {
		return "", fmt.Errorf("sync: cannot rewrite eka.yaml with the aligned namespace: %w", err)
	}
	return fmt.Sprintf("repository namespace aligned: %s → %s (eka.yaml updated; identity frozen again)", oldNS, contentNS), nil
}

// Run executes one sync of the repository at repoPath: resolve the
// repository context, resolve and (auto-)register the repository, then
// pull and/or push per opts. Errors are wrapped with context;
// validation and integrity failures carry their typed classes (see
// pull.go).
//
// Repository context gate (ADR-018): an EKA repository is a directory
// tree carrying eka.yaml, located by walking up from repoPath (the
// nearest file wins). When the walk-up finds no eka.yaml, the sync is
// REFUSED deterministically — <abs> is not an EKA repository; run
// 'eka init' first. There is no legacy mode: basename auto-registration
// is removed. The syncing repository ROOT is the walk-up directory
// that carries eka.yaml — not necessarily the given path: a run from a
// subdirectory (e.g. <repo>/docs) syncs the repository root, so
// subdirectory invocations can never re-point the registry or write
// stray snapshot directories.
//
// Repository resolution (ADR-017): with eka.yaml present, the
// repository is resolved by its identity — the registry is looked up
// by (project, name); a miss auto-registers with the metadata identity
// (project, name, namespace written immediately). The stored path is
// refreshed to the repository root on every sync (a worktree sync
// re-points the auxiliary path). The identity pair (project,
// namespace) is immutable: a metadata namespace that differs from the
// registered namespace is refused.
//
// Content namespace reconciliation (ADR-020 Decision 3): the CONTENT
// namespace is authoritative for unit identity (P3) — the detection
// runs BEFORE any registration, pull or store write, reading the
// repository's content namespaces from the SOURCE (precedence: the
// snapshot package when one exists and FromDocs is false — verified
// byte-exact exactly like the pull; else the docs tree scan
// (conformance.Scan); else the canonical store units of the repository
// identity, the push-only fallback that preserves the seeded-store
// behavior). Content spanning MULTIPLE distinct namespaces is refused
// without override (a repository is one platform — consolidate the
// content). Exactly ONE distinct content namespace differing from the
// declared eka.yaml namespace (m.Namespace — the registry row may not
// exist yet, so the declared value is the file's, not the row's) is
// refused with the override hint (opts.Override), or aligned when
// opts.Override is set (or the injected opts.Confirm chooses the align
// option interactively): eka.yaml namespace rewritten (project/name
// untouched), then the resolution step records the aligned namespace
// on the registered row (SetRepoNamespace — the sanctioned identity
// change, replacing the immutability check) or in the registration
// INSERT. A refused run leaves the STORE untouched (no registration,
// no seeded units, no snapshot); the aligned note is appended to the
// report warnings. A repository with no content and no stored units is
// unaffected.
func Run(ws *workspace.Workspace, repoPath string, opts Options) (*Report, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return nil, fmt.Errorf("sync: cannot resolve repository path %q: %w", repoPath, err)
	}
	abs = filepath.Clean(abs)

	report := &Report{Workspace: ws.Path()}

	// The repository context gate (ADR-018): an EKA repository is a
	// directory tree carrying eka.yaml — the walk-up finds it, and
	// without it the tree is not an EKA repository. Deterministic
	// refusal, git-style strictness; 'eka init' is the generator.
	m, mdir, hasMeta, err := metadata.Find(abs)
	if err != nil {
		return nil, err
	}
	if !hasMeta {
		return nil, fmt.Errorf("sync refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
	}

	// The walk-up directory is the canonical repository root: every
	// registry/filesystem touch below (identity lookup, path refresh,
	// auto-registration, and via repo.Path the pull/push snapshot
	// directory) operates on it, never on the user-given abs — a run
	// from a subdirectory syncs the repository root.
	root := mdir

	// Content namespace reconciliation detection (ADR-020 Decision 3):
	// the CONTENT namespace is authoritative for unit identity (P3) —
	// the detection runs HERE, BEFORE any registration, pull or store
	// write, so a refused run leaves the store untouched (D3: no
	// changes written on refusal). The namespaces are read from the
	// source (snapshot package → docs scan → store units fallback);
	// the declared namespace compared against is m.Namespace (the
	// eka.yaml value — the registry row may not exist yet).
	nsList, err := sourceContentNamespaces(ws, root, m, opts)
	if err != nil {
		return nil, err
	}
	aligned := false
	switch {
	case len(nsList) >= 2:
		// Multi-ns content: refusal without override — a repository
		// is one platform, consolidate the content first.
		return nil, namespaceMultiError(nsList)
	case len(nsList) == 1 && nsList[0] != m.Namespace:
		contentNS := nsList[0]
		align := opts.Override
		if !align && opts.Confirm != nil {
			// Interactive confirmation (TTY): the arrow-selected
			// options are "align identity to <contentNS>" (default)
			// and "abort" — abort yields the same deterministic
			// refusal, exit 2.
			idx, err := opts.Confirm(
				"the repository content namespace "+contentNS+" differs from the registered repository namespace "+m.Namespace,
				[]string{"align identity to " + contentNS, "abort"},
				0)
			if err != nil {
				return nil, fmt.Errorf("sync refused: %w", err)
			}
			align = idx == 0
		}
		if !align {
			return nil, namespaceMismatchError(contentNS, m.Namespace)
		}
		// Alignment (no store write here — the row may not exist
		// yet): eka.yaml rewritten with the content namespace
		// (project/name untouched), the in-memory metadata carries
		// the aligned namespace into the resolution step below, and
		// the aligned note lands in the report warnings
		// (deterministic order).
		oldNS := m.Namespace
		note, err := rewriteEKANamespace(root, oldNS, contentNS)
		if err != nil {
			return nil, err
		}
		m.Namespace = contentNS
		aligned = true
		report.Warnings = append(report.Warnings, note)
	}

	// Resolve the repository identity (ADR-017): eka.yaml metadata is
	// the identity (project + name; the stored path is auxiliary — a
	// worktree resolves to the same repository).
	var repo workspace.Repo
	var found bool
	repo, found, err = ws.FindRepo(root)
	if err != nil {
		return nil, err
	}
	if found {
		if aligned {
			// The sanctioned identity change (ADR-020 D3): the
			// override replaced the immutability check — the
			// aligned content namespace is recorded on the
			// EXISTING row (the override is the sanctioned way the
			// frozen pair changes).
			if err := ws.Store().SetRepoNamespace(repo.ProjectID, repo.Name, m.Namespace); err != nil {
				return nil, fmt.Errorf("sync: cannot record the aligned repository namespace: %w", err)
			}
			repo.Namespace = m.Namespace
		} else {
			// The identity pair (project, namespace) is frozen after
			// the first sync: record the metadata namespace when the
			// registry has none yet, refuse a drift otherwise.
			if repo.Namespace == "" {
				if err := ws.Store().SetRepoNamespace(repo.ProjectID, repo.Name, m.Namespace); err != nil {
					return nil, fmt.Errorf("sync: cannot record repository namespace: %w", err)
				}
				repo.Namespace = m.Namespace
			} else if repo.Namespace != m.Namespace {
				return nil, namespaceImmutableError(m.Namespace, repo.Namespace)
			}
		}
		// Path refresh: the syncing directory is the repository's
		// current location (a worktree sync re-points the
		// auxiliary path; the resolver key stays the identity).
		// The in-memory record is carried along so pull/push below
		// operate on the refreshed root.
		if repo.Path != root {
			if _, err := ws.Store().DB().Exec(
				`UPDATE repos SET path = ? WHERE project_id = ? AND name = ?`,
				root, repo.ProjectID, repo.Name); err != nil {
				return nil, fmt.Errorf("sync: cannot refresh repository path: %w", err)
			}
			repo.Path = root
		}
		report.Project = repo.ProjectID
	} else {
		// Auto-register with the metadata identity (project, name,
		// namespace from eka.yaml — never the basename); the
		// registered path is the repository root, not the argument.
		// When the detection aligned the identity above, m.Namespace
		// already carries the content namespace — the registration
		// INSERT writes the aligned namespace.
		var project workspace.Project
		project, repo, _, err = ws.RegisterRepoMetadata(root, m)
		if err != nil {
			return nil, err
		}
		report.Project = project.ID
		report.NewRepo = true
		// The registration INSERT wrote the namespace; carry it on
		// the resolved record for the pull/push below.
		repo.Namespace = m.Namespace
	}
	report.Repo = repo.Name

	if opts.Pull {
		result, err := Pull(ws, repo, opts.FromDocs)
		if err != nil {
			return nil, err
		}
		report.PullSource = result.Source
		report.PulledUnits = result.Units
		report.PulledAttachments = result.Attachments
		report.Unchanged = result.Unchanged
		report.Overwrites = result.Overwrites
		report.KeptNewer = result.KeptNewer
		if result.Digest != "" {
			report.SnapshotDigest = result.Digest
		}
	}

	if opts.Push {
		result, err := Push(ws, repo)
		if err != nil {
			return nil, err
		}
		report.PushedUnits = result.Units
		report.PushedAttachments = result.Attachments
		report.PushChanged = result.Changed
		if result.Label != "" {
			report.SnapshotLabel = result.Label
		}
		if result.Digest != "" {
			report.SnapshotDigest = result.Digest
		}
	}

	if report.Unchanged && !report.PushChanged {
		report.Warnings = append(report.Warnings, "no changes: snapshot already up to date")
	}
	if report.PushChanged {
		report.Warnings = append(report.Warnings,
			"snapshot rewritten: digest "+report.SnapshotDigest+" differs from the replaced snapshot; the canonical store and the repository were out of sync (run 'eka integrity check' to verify the store)")
	}
	for _, o := range report.Overwrites {
		report.Warnings = append(report.Warnings, o)
	}
	for _, k := range report.KeptNewer {
		report.Warnings = append(report.Warnings, k)
	}
	return report, nil
}
