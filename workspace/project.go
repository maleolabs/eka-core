package workspace

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/maleolabs/eka-core/metadata"
)

// This file implements the project + repository registry over the
// workspace database (store). A Project groups one or more
// repositories; a Repo is one registered repository with its registry
// name.
//
// Registry semantics:
//   - project id = name; for a legacy repository (no eka.yaml) both are
//     the flag value or the basename of the absolute repository path;
//     for a repository with identity metadata (eka.yaml, ADR-017) both
//     come from the metadata file (project = m.Project, name = m.Name)
//     — the basename is never used when metadata is present;
//   - the repository path is normalized to an absolute clean path;
//   - a repository path is owned by the project that registered it
//     first: re-registering the path under the same project is a no-op
//     (idempotent), re-registering it under a different project is
//     refused (deterministic ownership — the path column is unique, so
//     provenance and sync resolution can never be ambiguous);
//   - the provenance pair of a repository is (project_id, name) — the
//     same composite key used by the canonical store, so objects and
//     attachments are attributed workspace-uniquely;
//   - repository resolution is identity-based when the path tree
//     carries eka.yaml: the metadata is located by walking up from the
//     path (nearest eka.yaml wins) and the registry is looked up by
//     (project_id, name) — the stored path is auxiliary (a git worktree
//     or a renamed directory resolves to the same repository). Without
//     metadata the legacy exact-path lookup applies.
//
// All SQL is parameterized.

// Project is one registered project.
type Project struct {
	ID      string
	Name    string
	Created string
}

// ReservedRepoName is the repository name reserved for workspace-native
// knowledge: the provenance sentinel the Authoring API records for
// published objects (source_repo = "runtime" in the canonical store,
// runtime/draft.go draftSourceRepo). A repository literally named
// "runtime" would otherwise pull workspace-native objects into its
// snapshot through the provenance pair (project_id, source_repo) —
// RegisterRepo and RegisterRepoMetadata refuse the name so the sentinel
// can never collide with a real repository.
const ReservedRepoName = "runtime"

// Repo is one registered repository.
type Repo struct {
	ProjectID string
	Name      string
	Path      string
	Created   string
	// Namespace is the repository's DEFAULT namespace (schema v3):
	// the platform-scoped identity prefix the authoring commands
	// resolve for unqualified targets inside this repository (spec §3.2).
	// It is recorded at registration from the identity metadata
	// (eka.yaml) when present, else '' (the legacy path, populated by
	// the sync engine at push time); an empty value means "not
	// resolved yet".
	Namespace string
}

// RegisterRepo registers the repository at path under a project. This
// is the LEGACY compatibility path: the repository name is the basename
// of the cleaned absolute path and the namespace is recorded as the
// empty string (the sync engine resolves it at push time). Repositories
// that carry identity metadata (eka.yaml) must be registered with
// RegisterRepoMetadata instead — the metadata identity (project, name,
// namespace) is the authority.
//
// The project name is the name flag value when non-empty, else the same
// basename; the project is created when missing (project id = name).
// The repo row (project_id, name) is upserted with the normalized
// absolute path; re-registering an already owned path under the same
// project updates nothing (idempotent). created reports whether the
// repo row was newly created. Two repositories with different basenames
// can therefore share one project (the same --name), which is how a
// project groups multiple repositories.
func (w *Workspace) RegisterRepo(path, name string) (Project, Repo, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot resolve repository path %q: %w", path, err)
	}
	abs = filepath.Clean(abs)
	repoName := filepath.Base(abs)
	if repoName == "" || repoName == "." || repoName == string(filepath.Separator) {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot derive a repository name from path %q", abs)
	}
	if repoName == ReservedRepoName {
		return Project{}, Repo{}, false, fmt.Errorf(
			"workspace: repository name %q is reserved for workspace-native knowledge", repoName)
	}
	projectName := name
	if projectName == "" {
		projectName = repoName
	}
	return w.register(path, projectName, repoName, "")
}

// RegisterRepoMetadata registers the repository at path under the
// identity recorded in its repository identity metadata (eka.yaml,
// ADR-017): project = m.Project, repository name = m.Name (never the
// path basename), and repos.namespace is written immediately with
// m.Namespace. The reserved-name rule (m.Name must not be "runtime")
// applies exactly as in RegisterRepo; the path-ownership rule is
// unchanged (a path owned by a different project refuses).
func (w *Workspace) RegisterRepoMetadata(path string, m metadata.Metadata) (Project, Repo, bool, error) {
	if m.Name == ReservedRepoName {
		return Project{}, Repo{}, false, fmt.Errorf(
			"workspace: repository name %q is reserved for workspace-native knowledge", m.Name)
	}
	return w.register(path, m.Project, m.Name, m.Namespace)
}

// register is the shared registration implementation behind
// RegisterRepo (legacy) and RegisterRepoMetadata (identity): the
// project is upserted (project id = projectName), the repo row
// (project_id, name) is upserted with the normalized absolute path and
// the given namespace, and the path-ownership rule applies (a path
// registered under a different project is refused — the first project
// owns it, registry determinism). The repository-name derivation
// (basename vs metadata name) stays in the public wrappers.
func (w *Workspace) register(path, projectName, repoName, namespace string) (Project, Repo, bool, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot resolve repository path %q: %w", path, err)
	}
	abs = filepath.Clean(abs)

	// Path ownership: a path registered under a different project is
	// refused — the first project owns it (registry determinism).
	if existing, found, err := findRepoByPath(w, abs); err != nil {
		return Project{}, Repo{}, false, err
	} else if found && existing.ProjectID != projectName {
		return Project{}, Repo{}, false, fmt.Errorf(
			"workspace: repository path %s is already registered under project %q; register it under that project or choose another path",
			abs, existing.ProjectID)
	}

	now := time.Now().Format("2006-01-02")

	// Upsert the project; read its record back.
	if _, err := w.Store().DB().Exec(`INSERT INTO projects (id, name, created) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name`, projectName, projectName, now); err != nil {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot register project %q: %w", projectName, err)
	}
	project := Project{ID: projectName, Name: projectName, Created: now}
	if err := w.Store().DB().QueryRow(`SELECT created FROM projects WHERE id = ?`, projectName).Scan(&project.Created); err != nil {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot read project %q: %w", projectName, err)
	}

	// Upsert the repo; created = whether a fresh row was inserted
	// (checked before the upsert, since the row exists afterwards).
	exists, err := repoExists(w, projectName, repoName)
	if err != nil {
		return Project{}, Repo{}, false, err
	}
	created := !exists
	// The namespace column is written at registration: m.Namespace when
	// the identity metadata provides it (RegisterRepoMetadata), else ''
	// (the legacy path, where the repository's default namespace is
	// resolved by the sync engine at push time, never by the registry).
	if _, err := w.Store().DB().Exec(`INSERT INTO repos (project_id, name, path, created, namespace) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(project_id, name) DO UPDATE SET path = excluded.path`, projectName, repoName, abs, now, namespace); err != nil {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot register repository %q: %w", abs, err)
	}
	repo := Repo{ProjectID: projectName, Name: repoName, Path: abs, Created: now}
	if err := w.Store().DB().QueryRow(`SELECT created FROM repos WHERE project_id = ? AND name = ?`, projectName, repoName).Scan(&repo.Created); err != nil {
		return Project{}, Repo{}, false, fmt.Errorf("workspace: cannot read repository record: %w", err)
	}
	return project, repo, created, nil
}

// UnregisterRepo removes the repository (projectID, name) from the
// registry: the repos row (project_id, name) is deleted and removed
// reports whether a row existed (false = it was not registered — not
// an error). The reserved provenance sentinel "runtime" is refused
// with the same deterministic error as registration (the sentinel can
// never be unregistered because it can never be registered).
//
// When the removed repository was the project's LAST repository, the
// now-empty project row is deleted too — a project exists only as a
// grouping of repositories, so removing the last member removes the
// group. Canonical knowledge objects are NOT touched: they stay in
// the store under their provenance pair, and re-registering the
// repository restores their provenance access.
func (w *Workspace) UnregisterRepo(projectID, name string) (bool, error) {
	if name == ReservedRepoName {
		return false, fmt.Errorf(
			"workspace: repository name %q is reserved for workspace-native knowledge", name)
	}
	res, err := w.Store().DB().Exec(`DELETE FROM repos WHERE project_id = ? AND name = ?`, projectID, name)
	if err != nil {
		return false, fmt.Errorf("workspace: cannot unregister repository %s/%s: %w", projectID, name, err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("workspace: cannot confirm the unregistration of %s/%s: %w", projectID, name, err)
	}
	if removed == 0 {
		return false, nil
	}
	// Last-repo cleanup: when no repositories remain under the
	// project, delete the empty project row (see the doc comment).
	var remaining int
	if err := w.Store().DB().QueryRow(`SELECT COUNT(*) FROM repos WHERE project_id = ?`, projectID).Scan(&remaining); err != nil {
		return false, fmt.Errorf("workspace: cannot count the repositories of project %q: %w", projectID, err)
	}
	if remaining == 0 {
		if _, err := w.Store().DB().Exec(`DELETE FROM projects WHERE id = ?`, projectID); err != nil {
			return false, fmt.Errorf("workspace: cannot remove the emptied project %q: %w", projectID, err)
		}
	}
	return true, nil
}

// repoExists reports whether a repo row already exists (checked before
// the upsert, so a pre-existing row means the upsert was an update).
func repoExists(w *Workspace, projectID, name string) (bool, error) {
	var n int
	err := w.Store().DB().QueryRow(`SELECT COUNT(*) FROM repos WHERE project_id = ? AND name = ?`, projectID, name).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("workspace: cannot check repository existence: %w", err)
	}
	return n > 0, nil
}

// findRepoByPath is the legacy exact-path registry lookup: the repos
// row whose path column equals the normalized absolute path, if any.
// The path column is unique, so at most one repository row can match.
func findRepoByPath(w *Workspace, abs string) (Repo, bool, error) {
	var r Repo
	err := w.Store().DB().QueryRow(`SELECT project_id, name, path, created, namespace FROM repos WHERE path = ?`, abs).
		Scan(&r.ProjectID, &r.Name, &r.Path, &r.Created, &r.Namespace)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repo{}, false, nil
		}
		return Repo{}, false, fmt.Errorf("workspace: cannot find repository %s: %w", abs, err)
	}
	return r, true, nil
}

// FindRepo resolves the repository for absPath. Resolution is
// identity-based when the path tree carries repository identity
// metadata (eka.yaml, ADR-017): the metadata is located by walking up
// from absPath (the nearest eka.yaml wins), and the registry is looked
// up by the identity pair (project_id, name) — the stored path is
// auxiliary, so a git worktree or a renamed directory resolves to the
// same repository. When no metadata exists above absPath, the legacy
// exact-path lookup applies (the path column is unique, so at most one
// repository row can match).
func (w *Workspace) FindRepo(absPath string) (Repo, bool, error) {
	abs, err := filepath.Abs(absPath)
	if err != nil {
		return Repo{}, false, fmt.Errorf("workspace: cannot resolve path %q: %w", absPath, err)
	}
	abs = filepath.Clean(abs)

	m, _, hasMeta, err := metadata.Find(abs)
	if err != nil {
		return Repo{}, false, fmt.Errorf("workspace: cannot resolve repository identity: %w", err)
	}
	if hasMeta {
		var r Repo
		err = w.Store().DB().QueryRow(`SELECT project_id, name, path, created, namespace FROM repos WHERE project_id = ? AND name = ?`, m.Project, m.Name).
			Scan(&r.ProjectID, &r.Name, &r.Path, &r.Created, &r.Namespace)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Repo{}, false, nil
			}
			return Repo{}, false, fmt.Errorf("workspace: cannot find repository %s/%s: %w", m.Project, m.Name, err)
		}
		return r, true, nil
	}
	return findRepoByPath(w, abs)
}

// Repos returns every repository of one project, sorted by name.
func (w *Workspace) Repos(projectID string) ([]Repo, error) {
	rows, err := w.Store().DB().Query(`SELECT project_id, name, path, created, namespace FROM repos
		WHERE project_id = ? ORDER BY name`, projectID)
	if err != nil {
		return nil, fmt.Errorf("workspace: cannot list repositories: %w", err)
	}
	defer rows.Close()
	var out []Repo
	for rows.Next() {
		var r Repo
		if err := rows.Scan(&r.ProjectID, &r.Name, &r.Path, &r.Created, &r.Namespace); err != nil {
			return nil, fmt.Errorf("workspace: cannot scan repository row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace: cannot list repositories: %w", err)
	}
	return out, nil
}

// Projects returns every registered project, sorted by id.
func (w *Workspace) Projects() ([]Project, error) {
	rows, err := w.Store().DB().Query(`SELECT id, name, created FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("workspace: cannot list projects: %w", err)
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Created); err != nil {
			return nil, fmt.Errorf("workspace: cannot scan project row: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("workspace: cannot list projects: %w", err)
	}
	return out, nil
}

// SortedProjectIDs returns the project ids sorted (deterministic
// iteration for consumers that aggregate across projects).
func SortedProjectIDs(projects []Project) []string {
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	sort.Strings(ids)
	return ids
}
