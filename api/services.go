package api

import (
	"github.com/maleolabs/eka-core/v2/contexts"
	"github.com/maleolabs/eka-core/v2/runtime"
)

// This file defines the service contracts of the public API and their
// façades over the internal runtime services, plus the re-exported
// contract types. Every method mirrors the concrete internal method it
// wraps exactly (same parameters, same result types, same errors) —
// the façade only removes the need to name the internal *runtime.Runtime.

// Workspace is the read side of the workspace registry and status
// service (runtime.WorkspaceService). Registry WRITES (RegisterRepo,
// UnregisterRepo, UnregisterProject) are deliberately deferred from v1:
// a consumer drives registration through Authoring.Sync (which
// auto-registers), not through the registry directly.
type Workspace interface {
	// Projects returns every registered project, sorted by id.
	Projects() ([]Project, error)
	// Repos returns every repository of one project, sorted by name.
	Repos(projectID string) ([]Repo, error)
	// FindRepo returns the repository registered at the normalized
	// absolute path absPath, if any.
	FindRepo(absPath string) (Repo, bool, error)
	// LastSync returns the most recent sync-log entry of one
	// repository, or false when it has none.
	LastSync(projectID, repo string) (*SyncEntry, bool, error)
	// Status aggregates the complete workspace overview in one call.
	Status() (*WorkspaceStatus, error)
}

// Knowledge is the Engineering-Knowledge read service
// (runtime.KnowledgeService). Per-repository reads (Units), single-object
// reads (Object), counts and issue numbering are deferred from v1 until
// a consumer requires them.
type Knowledge interface {
	// UnitsByProject returns the complete Engineering Knowledge of one
	// project, sorted by canonical form.
	UnitsByProject(projectID string) ([]*KnowledgeUnit, error)
	// Search filters the complete Engineering Knowledge of one project
	// by the exact-match criteria of the query, sorted by canonical
	// form.
	Search(query SearchQuery) ([]*KnowledgeUnit, error)
}

// Resolver is the identity resolution service (runtime.ResolverService).
type Resolver interface {
	// Resolve resolves one reference (canonical "<ns>/<type>:<id>:<v>"
	// or qualified line "<ns>/<type>:<id>") to its current unit.
	Resolve(form string) (*KnowledgeUnit, bool, error)
	// ResolveLine returns every instance of one artifact line, sorted
	// by instance-version (ascending). The public replacement for a
	// direct store.UnitsByLine read.
	ResolveLine(ns, typeToken, id string) ([]*KnowledgeUnit, error)
}

// Integrity is the store integrity verification service
// (runtime.IntegrityService).
type Integrity interface {
	// Verify scans the whole store and reports every integrity
	// violation (read-only).
	Verify() (*IntegrityReport, error)
	// SchemaVersion returns the canonical store schema version.
	SchemaVersion() (int, error)
}

// Authoring is the stateless compiler/validation/publish/sync write
// service (runtime.Authoring). It is the write side of the contract:
// scaffold (NewDraft), publish (Publish, PublishBatch), mutate published
// lines (Transition, Relate), notes (NoteDraft) and synchronize
// (Sync, SyncAdopt). Methods that the internal service exposes against
// other authoring representations (Compile, NewDraftBatch, PublishInline,
// NoteReply, ResolveNote, ResolveAllNotes, DraftsByProvenance) are
// deferred until a consumer requires them.
type Authoring interface {
	// Validate runs the authoring conformance gate over the repository
	// rooted at root and returns the report.
	Validate(root string) (*ValidationReport, error)
	// NewDraft scaffolds one draft in the workspace drafts tree.
	NewDraft(req NewDraftRequest) (*Draft, error)
	// Publish publishes one draft as an immutable Canonical Knowledge
	// Object (all-or-nothing).
	Publish(target string, opts PublishOptions) (*PublishResult, error)
	// PublishBatch publishes every pending draft of a project in
	// topological order.
	PublishBatch(opts PublishBatchOptions) (*PublishBatchResult, error)
	// Transition performs one forward/backward state transition.
	Transition(req TransitionRequest) (*TransitionResult, error)
	// Relate adds relationships to a published line (re-points it).
	Relate(req RelateRequest) (*RelateResult, error)
	// NoteDraft creates one cmt- note draft against a target.
	NoteDraft(req NoteDraftRequest) (*NoteDraftResult, error)
	// ResolveDraft locates one draft file by identity.
	ResolveDraft(target, projectHint string) (DraftRef, error)
	// Drafts lists the draft backlog of one project ("" = every
	// project), deterministically ordered.
	Drafts(project string) ([]Draft, error)
	// ValidateDraft re-validates one draft (CKO-level, no persistence).
	ValidateDraft(target, projectHint string) (*DraftValidation, error)
	// DiscardDraft deletes one draft file without publishing.
	DiscardDraft(target, projectHint string, force bool) (string, error)
	// Sync runs one synchronization of the repository at repoPath.
	Sync(repoPath string, opts SyncOptions) (*SyncResult, error)
	// SyncAdopt re-attributes the workspace-native units of the
	// repository's project to the repository provenance (ADR-032 C2).
	SyncAdopt(repoPath string, targets []string, dryRun bool) (*SyncAdoptResult, error)
}

// Contexts is the deterministic Context Engine read service
// (contexts.Engine). Build assembles the bounded context of one subject
// at a depth (local | dependency | engineering). The depth vocabulary and
// the result types are re-exported below so a consumer names them through
// this package only.
type Contexts interface {
	// Build assembles the context of one subject (a reference or short
	// form) of project projectID at the given depth.
	Build(subject, projectID string, depth ContextDepth, opts ContextOptions) (*ContextObject, error)
}

// --- Re-exported Context Engine types and vocabulary. ---

// Context Engine types.
type (
	// ContextDepth is a context depth (local | dependency | engineering).
	ContextDepth = contexts.Depth
	// ContextOptions configures one Build run.
	ContextOptions = contexts.Options
	// ContextObject is the assembled context of one subject.
	ContextObject = contexts.Object
)

// ParseDepth parses a depth name into a ContextDepth; ok is false for an
// unknown name. Mirror of contexts.ParseDepth.
func ParseDepth(s string) (ContextDepth, bool) { return contexts.ParseDepth(s) }

// Depths returns the valid depth names in render order. Mirror of
// contexts.Depths.
func Depths() []string { return contexts.Depths() }

// contextsFacade adapts contexts.Engine to the Contexts contract.
type contextsFacade struct{ rt *runtime.Runtime }

func (c contextsFacade) Build(subject, projectID string, depth ContextDepth, opts ContextOptions) (*ContextObject, error) {
	return contexts.New(c.rt).Build(subject, projectID, depth, opts)
}

// Registry and status.
type (
	// Project is one registered project.
	Project = runtime.Project
	// Repo is one registered repository.
	Repo = runtime.Repo
	// SyncEntry is one recorded pull or push run.
	SyncEntry = runtime.SyncEntry
	// WorkspaceStatus is the aggregated workspace overview.
	WorkspaceStatus = runtime.WorkspaceStatus
	// ProjectStatus is one project of the status aggregation.
	ProjectStatus = runtime.ProjectStatus
	// RepoStatus is one repository of the status aggregation.
	RepoStatus = runtime.RepoStatus
)

// Knowledge read.
type (
	// SearchQuery filters the Knowledge search.
	SearchQuery = runtime.SearchQuery
)

// Authoring request/result types.
type (
	// ValidationReport is the outcome of one Validate run.
	ValidationReport = runtime.ValidationReport
	// Draft is one scaffolded draft.
	Draft = runtime.Draft
	// DraftRef locates one draft file.
	DraftRef = runtime.DraftRef
	// DraftValidation is the outcome of one draft re-validation.
	DraftValidation = runtime.DraftValidation
	// NewDraftRequest describes one draft to scaffold.
	NewDraftRequest = runtime.NewDraftRequest
	// PublishOptions configures one publish run.
	PublishOptions = runtime.PublishOptions
	// PublishResult reports one successful publish.
	PublishResult = runtime.PublishResult
	// PublishBatchOptions configures one batch publish run.
	PublishBatchOptions = runtime.PublishBatchOptions
	// PublishBatchResult reports one batch publish run.
	PublishBatchResult = runtime.PublishBatchResult
	// TransitionRequest describes one state transition.
	TransitionRequest = runtime.TransitionRequest
	// TransitionResult reports one successful transition.
	TransitionResult = runtime.TransitionResult
	// RelateRequest describes one relationship write.
	RelateRequest = runtime.RelateRequest
	// RelateResult reports one successful relationship write.
	RelateResult = runtime.RelateResult
	// NoteDraftRequest describes one note draft.
	NoteDraftRequest = runtime.NoteDraftRequest
	// NoteDraftResult reports one created note draft.
	NoteDraftResult = runtime.NoteDraftResult
	// SyncOptions configures one sync run.
	SyncOptions = runtime.SyncOptions
	// SyncResult is the outcome of one sync run.
	SyncResult = runtime.SyncResult
	// SyncAdoptResult is the outcome of one adopt run.
	SyncAdoptResult = runtime.SyncAdoptResult
	// IntegrityReport is the outcome of one integrity verification.
	IntegrityReport = runtime.IntegrityReport
)

// Typed errors (aliases, so errors.As works with the api package named
// in the consumer).
type (
	// PublishError reports a draft that failed CKO-level validation.
	PublishError = runtime.PublishError
	// DraftNotFoundError reports a missing draft file.
	DraftNotFoundError = runtime.DraftNotFoundError
	// TransitionRefusal reports a refused state transition.
	TransitionRefusal = runtime.TransitionRefusal
	// RelateRefusal reports a refused relationship write.
	RelateRefusal = runtime.RelateRefusal
	// RelateValidationError reports a relationship write that failed
	// CKO-level validation (carries the report).
	RelateValidationError = runtime.RelateValidationError
	// ValidationError reports a repository that failed the authoring
	// conformance gate.
	ValidationError = runtime.ValidationError
)

// --- Service façades. ---

// workspaceFacade adapts runtime.WorkspaceService to the Workspace
// contract.
type workspaceFacade struct{ rt *runtime.Runtime }

func (w workspaceFacade) Projects() ([]Project, error) { return w.rt.Workspace.Projects() }
func (w workspaceFacade) Repos(projectID string) ([]Repo, error) {
	return w.rt.Workspace.Repos(projectID)
}
func (w workspaceFacade) FindRepo(absPath string) (Repo, bool, error) {
	return w.rt.Workspace.FindRepo(absPath)
}
func (w workspaceFacade) LastSync(projectID, repo string) (*SyncEntry, bool, error) {
	return w.rt.Workspace.LastSync(projectID, repo)
}
func (w workspaceFacade) Status() (*WorkspaceStatus, error) { return w.rt.Workspace.Status() }

// knowledgeFacade adapts runtime.KnowledgeService to the Knowledge
// contract.
type knowledgeFacade struct{ rt *runtime.Runtime }

func (k knowledgeFacade) UnitsByProject(projectID string) ([]*KnowledgeUnit, error) {
	return k.rt.Knowledge.UnitsByProject(projectID)
}
func (k knowledgeFacade) Search(query SearchQuery) ([]*KnowledgeUnit, error) {
	return k.rt.Knowledge.Search(query)
}

// resolverFacade adapts runtime.ResolverService to the Resolver
// contract.
type resolverFacade struct{ rt *runtime.Runtime }

func (r resolverFacade) Resolve(form string) (*KnowledgeUnit, bool, error) {
	return r.rt.Resolver.Resolve(form)
}
func (r resolverFacade) ResolveLine(ns, typeToken, id string) ([]*KnowledgeUnit, error) {
	return r.rt.Resolver.ResolveLine(ns, typeToken, id)
}

// integrityFacade adapts runtime.IntegrityService to the Integrity
// contract.
type integrityFacade struct{ rt *runtime.Runtime }

func (i integrityFacade) Verify() (*IntegrityReport, error) { return i.rt.Integrity.Verify() }
func (i integrityFacade) SchemaVersion() (int, error)       { return i.rt.Integrity.SchemaVersion() }

// authoringFacade adapts the stateless runtime.Authoring service to the
// Authoring contract, binding the runtime the internal service expects
// as its first argument.
type authoringFacade struct{ rt *runtime.Runtime }

func (a authoringFacade) Validate(root string) (*ValidationReport, error) {
	return runtime.Authoring.Validate(root)
}
func (a authoringFacade) NewDraft(req NewDraftRequest) (*Draft, error) {
	return runtime.Authoring.NewDraft(a.rt, req)
}
func (a authoringFacade) Publish(target string, opts PublishOptions) (*PublishResult, error) {
	return runtime.Authoring.Publish(a.rt, target, opts)
}
func (a authoringFacade) PublishBatch(opts PublishBatchOptions) (*PublishBatchResult, error) {
	return runtime.Authoring.PublishBatch(a.rt, opts)
}
func (a authoringFacade) Transition(req TransitionRequest) (*TransitionResult, error) {
	return runtime.Authoring.Transition(a.rt, req)
}
func (a authoringFacade) Relate(req RelateRequest) (*RelateResult, error) {
	return runtime.Authoring.Relate(a.rt, req)
}
func (a authoringFacade) NoteDraft(req NoteDraftRequest) (*NoteDraftResult, error) {
	return runtime.Authoring.NoteDraft(a.rt, req)
}
func (a authoringFacade) ResolveDraft(target, projectHint string) (DraftRef, error) {
	return runtime.Authoring.ResolveDraft(a.rt, target, projectHint)
}
func (a authoringFacade) Drafts(project string) ([]Draft, error) {
	return runtime.Authoring.Drafts(a.rt, project)
}
func (a authoringFacade) ValidateDraft(target, projectHint string) (*DraftValidation, error) {
	return runtime.Authoring.ValidateDraft(a.rt, target, projectHint)
}
func (a authoringFacade) DiscardDraft(target, projectHint string, force bool) (string, error) {
	return runtime.Authoring.DiscardDraft(a.rt, target, projectHint, force)
}
func (a authoringFacade) Sync(repoPath string, opts SyncOptions) (*SyncResult, error) {
	return runtime.Authoring.Sync(a.rt, repoPath, opts)
}
func (a authoringFacade) SyncAdopt(repoPath string, targets []string, dryRun bool) (*SyncAdoptResult, error) {
	return runtime.Authoring.SyncAdopt(a.rt, repoPath, targets, dryRun)
}
