package runtime

import (
	"fmt"

	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the WorkspaceService: the workspace registry,
// the sync-log read side and the aggregated status view. It wraps the
// workspace package (registry semantics unchanged) and the sync log of
// the canonical store, re-exporting their contract types.

// Project is one registered project (re-exported workspace contract
// type).
type Project = workspace.Project

// Repo is one registered repository (re-exported workspace contract
// type).
type Repo = workspace.Repo

// SyncEntry is one recorded pull or push run (re-exported store
// contract type).
type SyncEntry = store.SyncEntry

// WorkspaceService is the registry and status service of the Runtime.
// It is concrete and documented — no interface type (no consumer needs
// polymorphism).
type WorkspaceService struct{ rt *Runtime }

// RegisterRepo registers the repository at path under a project
// (registry semantics of workspace.RegisterRepo: name = basename,
// project = name flag value or the same basename, first project owns
// the path). created reports whether the repo row was newly created.
func (s *WorkspaceService) RegisterRepo(path, name string) (Project, Repo, bool, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return Project{}, Repo{}, false, err
	}
	return ws.RegisterRepo(path, name)
}

// UnregisterRepo removes the repository (projectID, name) from the
// registry (registry semantics of workspace.UnregisterRepo: the repos
// row is deleted, removed reports whether it existed, the reserved
// name "runtime" is refused, and removing a project's LAST repository
// deletes the empty project row too — canonical knowledge objects
// stay in the store).
func (s *WorkspaceService) UnregisterRepo(projectID, name string) (bool, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return false, err
	}
	return ws.UnregisterRepo(projectID, name)
}

// UnregisterProject removes the whole project (registry semantics of
// workspace.UnregisterProject): every repository row under projectID
// is deleted and the project row is deleted too; the count reports how
// many repositories were removed (0 = none or unknown project).
// Canonical knowledge objects stay in the store.
func (s *WorkspaceService) UnregisterProject(projectID string) (int, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return 0, err
	}
	return ws.UnregisterProject(projectID)
}

// FindRepo returns the repository registered at absPath (normalized
// absolute), if any.
func (s *WorkspaceService) FindRepo(absPath string) (Repo, bool, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return Repo{}, false, err
	}
	return ws.FindRepo(absPath)
}

// Repos returns every repository of one project, sorted by name.
func (s *WorkspaceService) Repos(projectID string) ([]Repo, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	return ws.Repos(projectID)
}

// Projects returns every registered project, sorted by id.
func (s *WorkspaceService) Projects() ([]Project, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	return ws.Projects()
}

// LastSync returns the most recent sync-log entry of one repository
// (the newest pull or push run), or false when the repository has no
// recorded sync.
func (s *WorkspaceService) LastSync(projectID, repo string) (*SyncEntry, bool, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, false, err
	}
	entries, err := st.RecentSyncs(projectID, repo, 1)
	if err != nil {
		return nil, false, err
	}
	if len(entries) == 0 {
		return nil, false, nil
	}
	return &entries[0], true, nil
}

// WorkspaceStatus is the aggregated workspace overview serving
// `eka status`: metadata, schema version, per-project repository
// bullets with their last sync, and the canonical store totals. Every
// collection is deterministically ordered (projects by id,
// repositories by name) — one call, one consistent snapshot.
type WorkspaceStatus struct {
	// Path is the absolute workspace root.
	Path string
	// ID and Created are the workspace.json metadata.
	ID      string
	Created string
	// SchemaVersion is the canonical store schema version (eka.db —
	// the meaningful one; the workspace.json file format version is an
	// internal detail).
	SchemaVersion int
	// Projects lists every registered project, sorted by id.
	Projects []ProjectStatus
	// Objects/Payloads/Attachments are the canonical store totals:
	// references (the current objects of the immutable model),
	// immutable payloads, attachments.
	Objects, Payloads, Attachments int
}

// ProjectStatus is one project of the status aggregation.
type ProjectStatus struct {
	// Project is the project record.
	Project Project
	// Repos lists the project's repositories, sorted by name.
	Repos []RepoStatus
}

// RepoStatus is one repository of the status aggregation.
type RepoStatus struct {
	// Repo is the repository record.
	Repo Repo
	// LastSync is the most recent sync-log entry of the repository
	// (nil when it has none).
	LastSync *SyncEntry
}

// Status aggregates the complete workspace overview in one call (the
// `eka status` service). Any store or registry failure is propagated:
// status is a monitoring command and must never report a false healthy
// state.
func (s *WorkspaceService) Status() (*WorkspaceStatus, error) {
	ws, err := s.rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}

	sv, err := st.SchemaVersion()
	if err != nil {
		return nil, fmt.Errorf("runtime: status: cannot read the schema version: %w", err)
	}
	_, id, created := ws.Meta()

	projects, err := ws.Projects()
	if err != nil {
		return nil, fmt.Errorf("runtime: status: %w", err)
	}
	objects, err := st.RefCount()
	if err != nil {
		return nil, fmt.Errorf("runtime: status: %w", err)
	}
	payloads, err := st.PayloadCount()
	if err != nil {
		return nil, fmt.Errorf("runtime: status: %w", err)
	}
	attachments, err := st.AttachmentCount()
	if err != nil {
		return nil, fmt.Errorf("runtime: status: %w", err)
	}

	out := &WorkspaceStatus{
		Path:          ws.Path(),
		ID:            id,
		Created:       created,
		SchemaVersion: sv,
		Objects:       objects,
		Payloads:      payloads,
		Attachments:   attachments,
		Projects:      make([]ProjectStatus, 0, len(projects)),
	}
	for _, p := range projects {
		repos, err := ws.Repos(p.ID)
		if err != nil {
			return nil, fmt.Errorf("runtime: status: %w", err)
		}
		ps := ProjectStatus{Project: p, Repos: make([]RepoStatus, 0, len(repos))}
		for _, r := range repos {
			last, _, err := s.LastSync(p.ID, r.Name)
			if err != nil {
				return nil, fmt.Errorf("runtime: status: %w", err)
			}
			ps.Repos = append(ps.Repos, RepoStatus{Repo: r, LastSync: last})
		}
		out.Projects = append(out.Projects, ps)
	}
	return out, nil
}
