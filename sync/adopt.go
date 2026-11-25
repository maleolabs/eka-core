package sync

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the adopt side of the sync engine (ADR-032,
// Option C2): the EXPLICIT re-attribution of workspace-native units to
// a repository. Units published via `eka publish` live in the
// canonical store under the workspace-native provenance sentinel
// (source_repo = "runtime" — workspace.ReservedRepoName); a clone of
// the repository on another device only receives the snapshot's units,
// so the workspace-native knowledge never travels. Adopt moves the
// REFERENCE of such units to the repository provenance
// (source_repo = repo.Name): the next push assembles them into the
// snapshot, and a clone on another device pulls them back.
//
// Model (documented):
//
//   - Adopt is a REFERENCE-only operation: the immutable payloads
//     (object_payloads rows) are content-addressed and are NEVER
//     touched — the re-attributed reference points at the same
//     object_hash, so a clone pulls byte-identical units. There is no
//     payload copy and no content re-hash.
//   - The reference row is the mutable state: re-attribution is a
//     DELETE of the workspace-native row followed by an INSERT of the
//     same row with source_repo = repo.Name (the safest form because
//     the reference's canonical form is the PRIMARY KEY: a provenance
//     change cannot be expressed as an UPDATE of a key-encoding
//     column, and the DELETE+INSERT keeps the mutation a single
//     atomic swap). Both statements run in ONE transaction; any error
//     rolls the whole adopt back. Processing order is deterministic
//     (sorted by canonical form).
//   - Conflict guard (never overwrite a repository payload): when a
//     reference with the SAME canonical form already exists under the
//     repository provenance, an identical object_hash is a MERGE (the
//     repository row is kept, the workspace-native row is dropped,
//     counted as adopted) and a DIFFERENT object_hash is REFUSED (the
//     unit lands in Skipped — the repository's existing payload is
//     never overwritten). In the current schema (form = canonical
//     identity, PRIMARY KEY) a runtime row and a repository row can
//     never coexist, so the conflict branch is defensive — it pins the
//     ADR-032 "do not overwrite" rule under the provenance-qualified
//     form model.
//   - There are no note gates and no validation gate: adopt is a pure
//     reference re-attribution, never a knowledge-quality check.
//
// Target resolution: without targets every workspace-native unit of
// the repository's project AND namespace is adopted (a unit whose
// namespace differs from the repository namespace is left in place and
// reported in Ignored — a repository is one platform, so
// foreign-namespace units never enter its snapshot); with targets each
// target is parsed with the Rule-5 reference grammar
// (conformance.ParseReference, `<namespace>/<type>:<id>` or
// `<type>:<id>`, optional `:<instance-version>` suffix), the namespace
// must equal the repository namespace (refusal otherwise), and every
// matching workspace-native unit of the line is adopted. A target
// matching no workspace-native unit is refused deterministically.
//
// Error classes: refusals (invalid target, namespace mismatch, no
// matching workspace-native unit) and internal failures are plain
// wrapped errors mapped to exit code 2 by the CLI — adopt never
// produces a validation or integrity failure class.

// AdoptResult is the outcome of one adopt run.
type AdoptResult struct {
	// Units counts the adopted units (re-attributed from the
	// workspace-native provenance to the repository provenance).
	Units int
	// Skipped lists the canonical forms whose re-attribution was
	// refused because the repository already references the identity
	// with a DIFFERENT payload (deterministic order, empty when none).
	Skipped []string
	// Ignored lists the canonical forms left under the workspace-native
	// provenance because their namespace differs from the repository
	// namespace (adopt-all only — a repository is one platform, so
	// foreign-namespace units never enter its snapshot; deterministic
	// order, empty when none).
	Ignored []string
	// DryRun reports whether the run was a dry run (result computed,
	// no store mutation).
	DryRun bool
}

// adoptRuntimeRef is one workspace-native reference row of the
// repository's project.
type adoptRuntimeRef struct {
	Form            string
	ObjectHash      string
	Namespace       string
	Type            string
	ID              string
	InstanceVersion int
}

// adoptRow is the full reference row of one workspace-native unit,
// read before the re-attribution so the DELETE + INSERT can copy every
// column verbatim (the payload is untouched — the object_hash is
// copied unchanged).
type adoptRow struct {
	Form            string
	ObjectHash      string
	ProjectID       string
	SourceRepo      string
	Namespace       string
	Type            string
	ID              string
	InstanceVersion int
	Revision        int
	Dimension       string
	Domain          string
	Phase           string
	UpdatedAt       string
	Number          int
	NumberGroup     string
}

// Adopt re-attributes the workspace-native units (source_repo =
// "runtime") of the repository's project to the repository provenance
// (source_repo = repo.Name) — ADR-032 Option C2. Without targets every
// workspace-native unit of the repository's namespace is adopted (a
// unit whose namespace differs from the repository namespace is left
// in place and reported in Ignored — a repository is one platform);
// with targets only the units matching them (see the file header for
// the target grammar). dryRun computes the identical result without
// touching the store. The mutation is one transaction: any error rolls
// the whole adopt back; the processing order is deterministic (sorted
// by canonical form).
func Adopt(w *workspace.Workspace, repo workspace.Repo, targets []string, dryRun bool) (*AdoptResult, error) {
	runtimeRefs, err := workspaceNativeRefs(w, repo.ProjectID)
	if err != nil {
		return nil, err
	}
	result := &AdoptResult{DryRun: dryRun}
	if len(targets) > 0 {
		runtimeRefs, err = selectTargets(repo, targets, runtimeRefs)
		if err != nil {
			return nil, err
		}
	} else {
		// Adopt-all namespace filter: a repository is one platform —
		// only the units of the repository's namespace are adopted;
		// foreign-namespace units stay workspace-native and are
		// reported in Ignored (deterministic order).
		var kept []adoptRuntimeRef
		for _, rt := range runtimeRefs {
			if rt.Namespace != repo.Namespace {
				result.Ignored = append(result.Ignored, rt.Form)
				continue
			}
			kept = append(kept, rt)
		}
		sort.Strings(result.Ignored)
		runtimeRefs = kept
	}
	if len(runtimeRefs) == 0 {
		return result, nil
	}

	// Conflict detection (read-only in both modes): the repository's
	// references, one query (form -> object_hash). A reference with the
	// same canonical form already exists under the repository
	// provenance. In the current schema (form = canonical identity,
	// PRIMARY KEY) this can never match a workspace-native row, so the
	// branch is defensive — it enforces the ADR-032 "never overwrite a
	// repository payload" rule under the provenance-qualified form
	// model.
	repoHashes := map[string]string{}
	rows, err := w.Store().DB().Query(
		`SELECT form, object_hash FROM object_refs WHERE project_id = ? AND source_repo = ?`,
		repo.ProjectID, repo.Name)
	if err != nil {
		return nil, fmt.Errorf("sync adopt failed: cannot read the repository references: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var form, hash string
		if err := rows.Scan(&form, &hash); err != nil {
			return nil, fmt.Errorf("sync adopt failed: cannot scan the repository reference row: %w", err)
		}
		repoHashes[form] = hash
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync adopt failed: cannot read the repository references: %w", err)
	}

	adopted, skipped := classifyAdopt(runtimeRefs, repoHashes)
	result.Skipped = skipped

	if dryRun {
		result.Units = len(adopted)
		return result, nil
	}

	// Re-attribution, one transaction: the workspace-native row is
	// read (every column), DELETEd, and re-INSERTed under the
	// repository provenance — the payload is untouched (the
	// object_hash is copied unchanged).
	tx, err := w.Store().DB().Begin()
	if err != nil {
		return nil, fmt.Errorf("sync adopt failed: cannot begin the re-attribution: %w", err)
	}
	defer tx.Rollback()

	for _, rt := range adopted {
		if hash, ok := repoHashes[rt.Form]; ok && hash == rt.ObjectHash {
			// Merge: the repository row already carries the payload —
			// only the workspace-native row is dropped.
			res, err := tx.Exec(`DELETE FROM object_refs
				WHERE form = ? AND project_id = ? AND source_repo = ?`,
				rt.Form, repo.ProjectID, workspace.ReservedRepoName)
			if err != nil {
				return nil, fmt.Errorf("sync adopt failed: cannot drop the workspace-native reference of %s: %w", rt.Form, err)
			}
			if n, err := res.RowsAffected(); err != nil || n == 0 {
				return nil, fmt.Errorf("sync adopt failed: the workspace-native reference of %s vanished mid-transaction", rt.Form)
			}
			continue
		}
		var row adoptRow
		err := tx.QueryRow(`SELECT
			form, object_hash, project_id, source_repo, namespace, type, id,
			instance_version, revision, dimension, domain, phase, updated_at,
			number, number_group
			FROM object_refs
			WHERE form = ? AND project_id = ? AND source_repo = ?`,
			rt.Form, repo.ProjectID, workspace.ReservedRepoName).Scan(
			&row.Form, &row.ObjectHash, &row.ProjectID, &row.SourceRepo, &row.Namespace, &row.Type, &row.ID,
			&row.InstanceVersion, &row.Revision, &row.Dimension, &row.Domain, &row.Phase, &row.UpdatedAt,
			&row.Number, &row.NumberGroup)
		if err != nil {
			return nil, fmt.Errorf("sync adopt failed: cannot read the workspace-native reference of %s: %w", rt.Form, err)
		}
		res, err := tx.Exec(`DELETE FROM object_refs
			WHERE form = ? AND project_id = ? AND source_repo = ?`,
			rt.Form, repo.ProjectID, workspace.ReservedRepoName)
		if err != nil {
			return nil, fmt.Errorf("sync adopt failed: cannot drop the workspace-native reference of %s: %w", rt.Form, err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return nil, fmt.Errorf("sync adopt failed: the workspace-native reference of %s vanished mid-transaction", rt.Form)
		}
		// The re-attributed row is byte-equivalent to a row pulled
		// from the repository (every index column copied verbatim);
		// updated_at is re-stamped — the provenance change is a
		// reference mutation.
		if _, err := tx.Exec(`INSERT INTO object_refs (
			form, object_hash, project_id, source_repo, namespace, type, id,
			instance_version, revision, dimension, domain, phase, updated_at,
			number, number_group
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			row.Form, row.ObjectHash, row.ProjectID, repo.Name, row.Namespace, row.Type, row.ID,
			row.InstanceVersion, row.Revision, row.Dimension, row.Domain, row.Phase, nowUTC(),
			row.Number, row.NumberGroup); err != nil {
			return nil, fmt.Errorf("sync adopt failed: cannot re-attribute %s: %w", rt.Form, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("sync adopt failed: cannot commit the re-attribution: %w", err)
	}
	result.Units = len(adopted)
	return result, nil
}

// classifyAdopt splits the workspace-native references into the
// adopted set and the skipped set against the repository's existing
// references (form -> object_hash): a reference whose form the
// repository does not own is adopted; a reference the repository owns
// with the SAME payload is a merge (adopted — the repository row
// stays); a reference the repository owns with a DIFFERENT payload is
// refused (skipped — the repository payload is never overwritten,
// ADR-032). Pure function: the merge/skip classification is testable
// without a store (the current schema's form PRIMARY KEY makes the
// coexistence of a runtime row and a repository row with the same form
// impossible, so the branch is defensive). The skipped list is sorted
// deterministically.
func classifyAdopt(runtimeRefs []adoptRuntimeRef, repoHashes map[string]string) (adopted []adoptRuntimeRef, skipped []string) {
	for _, rt := range runtimeRefs {
		if hash, ok := repoHashes[rt.Form]; ok {
			if hash == rt.ObjectHash {
				// Merge: the repository already references the SAME
				// payload — the workspace-native row is dropped, the
				// repository row stays, counted as adopted.
				adopted = append(adopted, rt)
			} else {
				// Conflict: the repository references a DIFFERENT
				// payload — refused, the repository payload is never
				// overwritten.
				skipped = append(skipped, rt.Form)
			}
			continue
		}
		adopted = append(adopted, rt)
	}
	sort.Strings(skipped)
	return adopted, skipped
}

// AdoptAt resolves the repository at repoPath (the adopt subset of
// Run's resolution — see resolveRepo) and re-attributes its
// workspace-native units. It is the entry point of the Authoring API's
// SyncAdopt. A dry run resolves the repository WITHOUT touching the
// store: no auto-registration, no namespace record, no path refresh.
func AdoptAt(ws *workspace.Workspace, repoPath string, targets []string, dryRun bool) (*AdoptResult, error) {
	repo, err := resolveRepo(ws, repoPath, dryRun)
	if err != nil {
		return nil, err
	}
	return Adopt(ws, repo, targets, dryRun)
}

// workspaceNativeRefs returns the workspace-native reference rows
// (source_repo = "runtime") of one project, sorted by canonical form
// (deterministic order).
func workspaceNativeRefs(w *workspace.Workspace, projectID string) ([]adoptRuntimeRef, error) {
	rows, err := w.Store().DB().Query(`SELECT
		form, object_hash, namespace, type, id, instance_version
		FROM object_refs
		WHERE project_id = ? AND source_repo = ? ORDER BY form`,
		projectID, workspace.ReservedRepoName)
	if err != nil {
		return nil, fmt.Errorf("sync adopt failed: cannot read the workspace-native units: %w", err)
	}
	defer rows.Close()
	var out []adoptRuntimeRef
	for rows.Next() {
		var rt adoptRuntimeRef
		if err := rows.Scan(&rt.Form, &rt.ObjectHash, &rt.Namespace, &rt.Type, &rt.ID, &rt.InstanceVersion); err != nil {
			return nil, fmt.Errorf("sync adopt failed: cannot scan the workspace-native reference row: %w", err)
		}
		out = append(out, rt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync adopt failed: cannot read the workspace-native references: %w", err)
	}
	return out, nil
}

// selectTargets narrows the workspace-native references to the units
// matching the given targets (Rule-5 reference grammar:
// `<namespace>/<type>:<id>` or `<type>:<id>`, optional
// `:<instance-version>` suffix). A target's namespace must equal the
// repository namespace; a target matching no workspace-native unit is
// refused deterministically. The result keeps the deterministic
// (sorted-by-form) order.
func selectTargets(repo workspace.Repo, targets []string, runtimeRefs []adoptRuntimeRef) ([]adoptRuntimeRef, error) {
	selected := map[string]adoptRuntimeRef{}
	for _, target := range targets {
		ref, err := conformance.ParseReference(target, repo.Namespace, "")
		if err != nil {
			return nil, fmt.Errorf("sync adopt refused: invalid target %q: %v", target, err)
		}
		if ref.Namespace == "" {
			// The repository has no resolved namespace: an unqualified
			// target cannot be resolved.
			return nil, fmt.Errorf("sync adopt refused: target %q carries no namespace and the repository has no resolved namespace; use a fully qualified target <namespace>/<type>:<id>", target)
		}
		if ref.Namespace != repo.Namespace {
			return nil, fmt.Errorf("sync adopt refused: target %s namespace %s differs from the repository namespace %s", target, ref.Namespace, repo.Namespace)
		}
		matched := false
		for _, rt := range runtimeRefs {
			if rt.Namespace != ref.Namespace || rt.Type != ref.Type || rt.ID != ref.ID {
				continue
			}
			if ref.HasVersion && rt.InstanceVersion != ref.Version {
				continue
			}
			selected[rt.Form] = rt
			matched = true
		}
		if !matched {
			return nil, fmt.Errorf("sync adopt refused: target %s has no workspace-native unit in project %s (source_repo %q)", target, repo.ProjectID, workspace.ReservedRepoName)
		}
	}
	out := make([]adoptRuntimeRef, 0, len(selected))
	for _, rt := range runtimeRefs {
		if _, ok := selected[rt.Form]; ok {
			out = append(out, rt)
		}
	}
	return out, nil
}

// resolveRepo resolves the repository context for an adopt run: the
// ADR-018 repository-context gate (a directory tree carrying eka.yaml,
// located by walking up — without it the tree is not an EKA repository
// and the run is refused deterministically), then the ADR-017 identity
// resolution: the registry is looked up by the metadata identity
// (FindRepo; a miss auto-registers with RegisterRepoMetadata), the
// namespace is recorded when the registry has none yet and a drift of
// the frozen identity pair is refused, and the stored path is
// refreshed to the walk-up root. It is the adopt subset of Run's
// resolution: the ADR-020 content-namespace reconciliation is NOT
// relevant — adopt reads no repository content (it re-attributes
// workspace-native references only), so there is no content namespace
// to reconcile against.
//
// dryRun resolves WITHOUT mutating the store: the three registry
// writes (namespace record, path refresh, auto-registration) are
// skipped and the repository record is built in memory from the
// metadata identity — the dry-run contract "without touching the
// store" covers the registry too, so a dry run on a fresh repository
// leaves it unregistered.
func resolveRepo(ws *workspace.Workspace, repoPath string, dryRun bool) (workspace.Repo, error) {
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return workspace.Repo{}, fmt.Errorf("sync adopt failed: cannot resolve repository path %q: %w", repoPath, err)
	}
	abs = filepath.Clean(abs)

	m, mdir, hasMeta, err := metadata.Find(abs)
	if err != nil {
		return workspace.Repo{}, err
	}
	if !hasMeta {
		return workspace.Repo{}, fmt.Errorf("sync adopt refused: %s is not an EKA repository (no eka.yaml); run 'eka init' first", abs)
	}
	// The walk-up directory is the canonical repository root: the
	// registry touches below operate on it, never on the given path
	// (a run from a subdirectory adopts the repository root).
	root := mdir

	repo, found, err := ws.FindRepo(root)
	if err != nil {
		return workspace.Repo{}, err
	}
	if found {
		// The identity pair (project, namespace) is frozen after the
		// first sync: record the metadata namespace when the registry
		// has none yet, refuse a drift otherwise.
		if repo.Namespace == "" {
			if !dryRun {
				if err := ws.Store().SetRepoNamespace(repo.ProjectID, repo.Name, m.Namespace); err != nil {
					return workspace.Repo{}, fmt.Errorf("sync adopt failed: cannot record repository namespace: %w", err)
				}
			}
			repo.Namespace = m.Namespace
		} else if repo.Namespace != m.Namespace {
			return workspace.Repo{}, fmt.Errorf("sync adopt refused: %w", namespaceImmutableError(m.Namespace, repo.Namespace))
		}
		// Path refresh: the adopt directory is the repository's
		// current location (a worktree adopt re-points the auxiliary
		// path; the resolver key stays the identity).
		if repo.Path != root {
			if !dryRun {
				if _, err := ws.Store().DB().Exec(
					`UPDATE repos SET path = ? WHERE project_id = ? AND name = ?`,
					root, repo.ProjectID, repo.Name); err != nil {
					return workspace.Repo{}, fmt.Errorf("sync adopt failed: cannot refresh repository path: %w", err)
				}
			}
			repo.Path = root
		}
		return repo, nil
	}

	if dryRun {
		// No registration: the in-memory record is built from the
		// metadata identity (project, name, namespace from eka.yaml —
		// never the basename), the same convention as Run.
		return workspace.Repo{ProjectID: m.Project, Name: m.Name, Namespace: m.Namespace, Path: root}, nil
	}

	// Auto-register with the metadata identity (project, name,
	// namespace from eka.yaml — never the basename), the same
	// convention as Run.
	_, repo, _, err = ws.RegisterRepoMetadata(root, m)
	if err != nil {
		return workspace.Repo{}, err
	}
	repo.Namespace = m.Namespace
	return repo, nil
}
