package runtime

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the batch authoring API of the draft-publish
// workflow (reference/spec-authoring-publish.md §4): NewDraftBatch
// scaffolds a set of related drafts in one invocation and PublishBatch
// publishes the project's pending drafts in topological order
// (referenced drafts first). The commands remain thin layers over these
// methods; the batch semantics — all-or-nothing scaffolding, the draft
// graph, cycle and dangling-reference refusal — live here, next to the
// single-draft lifecycle they compose.
//
// The batch draft graph: the pending drafts of one project are the
// nodes; a relationship target that addresses another pending draft is
// an edge (the referenced draft must be published first). PublishBatch
// refuses deterministically before publishing anything when the graph
// contains a cycle (*BatchCycleError) or a reference that resolves
// neither to a pending draft nor to a published object
// (*BatchUnresolvedError). A publish that then fails CKO-level
// validation stops the run: the objects already published stay
// (immutability), the remaining drafts stay pending.

// BatchDraft describes one target of a batch authoring run — the same
// per-draft fields the single-target NewDraftRequest carries, with the
// content supplied inline instead of through a file path.
type BatchDraft struct {
	// Type is a known EKA artifact type token; ID is the draft id.
	Type string
	ID   string
	// Dimension and Phase are optional classification/context
	// frontmatter fields (phase is scp-/plan- only).
	Dimension, Phase string
	// Domain is the optional declared Engineering Domain (see
	// NewDraftRequest.Domain).
	Domain string
	// Relationships are the draft's authoring references, stored
	// verbatim (see NewDraftRequest.Relationships).
	Relationships []exchange.Relationship
	// Content is the inline content object merged over the type's
	// required-section placeholders (the inline counterpart of
	// NewDraftRequest.ContentFile). Nil scaffolds the placeholders.
	Content map[string]any
}

// NewDraftBatchRequest describes one batch scaffold run: the shared
// project/namespace/authority plus the targets, scaffolded in
// declaration order.
type NewDraftBatchRequest struct {
	// Project is the required project scope of every draft in the batch
	// (the drafts parent directory).
	Project string
	// Namespace is the required frontmatter namespace of every draft.
	Namespace string
	// By is the change-log authority of every draft's initial entries
	// (an empty identity = the default "Engineering" user).
	By conformance.AuthorIdentity
	// Drafts are the batch targets, scaffolded in declaration order.
	// Identities must be unique within the batch.
	Drafts []BatchDraft
}

// NewDraftBatchResult reports one successful batch scaffold.
type NewDraftBatchResult struct {
	// Created lists the scaffolded drafts in declaration order.
	Created []*Draft
}

// NewDraftBatch scaffolds a set of related drafts in one invocation.
// Every draft is scaffolded in declaration order through the shared
// newDraftFile pipeline (the same template, guards and O_EXCL collision
// guard `eka new` uses), so the batch honors the same per-target rules:
// a tkt- target must derive from a container, a ctr- target must depend
// on a plan-, `phase` is scp-/plan- only.
//
// All-or-nothing (spec §5.1 applied to the set): when any draft cannot
// be scaffolded — a collision with an existing draft, an unknown type,
// an empty id, a guard violation — the run refuses and removes the
// drafts it created, so a refused batch leaves no partial set behind.
// The refusal names the failing target and the number of drafts rolled
// back.
func (AuthoringService) NewDraftBatch(rt *Runtime, req NewDraftBatchRequest) (*NewDraftBatchResult, error) {
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	if len(req.Drafts) == 0 {
		return nil, fmt.Errorf("authoring: a batch requires at least one draft")
	}
	if req.Project == "" {
		return nil, fmt.Errorf("authoring: a batch requires a project")
	}
	if req.Namespace == "" {
		return nil, fmt.Errorf("authoring: a batch requires a namespace")
	}
	// Batch-level identity uniqueness (deterministic refusal before any
	// file is written): the spec §2.2 collision rule applies within the
	// batch, not only against existing drafts.
	seen := make(map[string]int, len(req.Drafts)) // "type:id" -> 1-based position
	for i, d := range req.Drafts {
		key := d.Type + ":" + d.ID
		if prev, dup := seen[key]; dup {
			return nil, fmt.Errorf("authoring: batch drafts %d and %d share the identity %s; batch identities must be unique",
				prev, i+1, key)
		}
		seen[key] = i + 1
	}

	var created []*Draft
	for i, d := range req.Drafts {
		draft, err := newDraftFile(ws, NewDraftRequest{
			Project:       req.Project,
			Namespace:     req.Namespace,
			Type:          d.Type,
			ID:            d.ID,
			Dimension:     d.Dimension,
			Phase:         d.Phase,
			Domain:        d.Domain,
			By:            req.By,
			Relationships: d.Relationships,
		}, d.Content)
		if err != nil {
			// All-or-nothing: remove the drafts this run created
			// (best-effort; the original error is the verdict).
			for _, c := range created {
				if rerr := os.Remove(c.Path); rerr != nil && !os.IsNotExist(rerr) {
					return nil, fmt.Errorf("authoring: batch draft %d of %d (%s:%s) cannot be scaffolded: %v; additionally the rollback of draft %s failed: %v",
						i+1, len(req.Drafts), d.Type, d.ID, err, c.Path, rerr)
				}
			}
			return nil, fmt.Errorf("authoring: batch draft %d of %d (%s:%s) cannot be scaffolded: %v; the %d draft(s) created by this run were removed",
				i+1, len(req.Drafts), d.Type, d.ID, err, len(created))
		}
		created = append(created, draft)
	}
	return &NewDraftBatchResult{Created: created}, nil
}

// --- PublishBatch ------------------------------------------------------

// PublishBatchOptions configures one batch publish run.
type PublishBatchOptions struct {
	// Project is the project scope whose pending drafts are published:
	// "" resolves the project from the repository registered at the
	// current working directory (the same rule Publish uses); an
	// explicit value addresses drafts/<project>/ from anywhere.
	Project string
}

// PublishBatchResult reports one batch publish run.
type PublishBatchResult struct {
	// Published lists the successful publishes in topological order.
	// Empty when the run published nothing (no pending drafts, or the
	// run refused before publishing).
	Published []*PublishResult
}

// BatchCycleError reports that the pending draft graph cannot be
// ordered: it contains a cycle (a draft transitively referencing
// itself). The run publishes nothing.
type BatchCycleError struct {
	// Drafts lists the draft identities (type:id) that cannot be
	// ordered: the cycle participants plus every draft that depends on
	// them, sorted deterministically.
	Drafts []string
}

// Error renders the deterministic refusal message.
func (e *BatchCycleError) Error() string {
	return fmt.Sprintf("publish refused: cycle among pending drafts: %s (referenced drafts must be published first)",
		strings.Join(e.Drafts, ", "))
}

// BatchUnresolvedError reports that a pending draft references a target
// that is neither a pending draft of the project nor an object already
// in the canonical store: a dangling reference to a non-draft. The run
// publishes nothing.
type BatchUnresolvedError struct {
	// Draft is the draft carrying the reference (type:id).
	Draft string
	// Target is the raw reference target that resolves nowhere.
	Target string
	// Detail is the deterministic reason: a reference parse error, or ""
	// for a well-formed reference that matches no pending draft and no
	// published object.
	Detail string
}

// Error renders the deterministic refusal message.
func (e *BatchUnresolvedError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("publish refused: draft %s references %q: %s", e.Draft, e.Target, e.Detail)
	}
	return fmt.Sprintf("publish refused: draft %s references %q, which is neither a pending draft nor a published object",
		e.Draft, e.Target)
}

// PublishBatch publishes every pending draft of a project in
// topological order: the draft graph is built from the drafts'
// relationship targets (all seven canonical fields), a referenced
// pending draft is published before the draft referencing it, and the
// order is otherwise deterministic (ties broken by identity).
//
// Pre-flight refusal — nothing is published — when:
//
//   - the graph contains a cycle (*BatchCycleError naming the drafts
//     that cannot be ordered); a self-reference is a length-1 cycle;
//   - a draft references a target that resolves neither to a pending
//     draft nor to an object already in the canonical store
//     (*BatchUnresolvedError naming the draft and the target): the
//     batch publish gate is stricter than Rule 5's draft tolerance,
//     because the published set must be coherent.
//
// An empty backlog is a valid no-op: an empty result, no error.
//
// The publish loop is per-draft atomic (Publish's spec §5.1 contract):
// each draft is validated and persisted one at a time, so a draft
// failing CKO-level validation stops the run — the objects already
// published stay (immutability), the failing draft and everything
// ordered after it stay pending. The failure propagates with Publish's
// error classes (*PublishError, *conformance.ScanError,
// *DraftNotFoundError) and the result carries the publishes completed
// so far.
func (AuthoringService) PublishBatch(rt *Runtime, opts PublishBatchOptions) (*PublishBatchResult, error) {
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	project := opts.Project
	if project == "" {
		project = cwdProjectOf(rt)
	}
	if project == "" {
		return nil, fmt.Errorf("publish: cannot resolve a project; run inside a registered repository")
	}
	drafts, err := Authoring.Drafts(rt, project)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	if len(drafts) == 0 {
		// An empty backlog is a valid no-op, not a refusal.
		return &PublishBatchResult{Published: []*PublishResult{}}, nil
	}

	// The pending graph. The frontmatter identity is the source of
	// truth (the same rule Publish enforces per draft), so the nodes
	// are keyed by the scanned artifact's namespace/type/id.
	nodes := make(map[string]*batchNode, len(drafts))
	for _, d := range drafts {
		a, err := conformance.ScanFile(d.Path)
		if err != nil {
			return nil, fmt.Errorf("publish: draft %s:%s cannot be read for batch ordering: %w", d.Type, d.ID, err)
		}
		if a == nil {
			return nil, fmt.Errorf("publish: draft %s:%s is not a knowledge artifact (missing type/id frontmatter)", d.Type, d.ID)
		}
		key := batchKey(a.Namespace, a.Type, a.ID)
		if _, dup := nodes[key]; dup {
			return nil, fmt.Errorf("publish: drafts in project %s share the identity %s", project, key)
		}
		nodes[key] = &batchNode{draft: d, artifact: a, deps: map[string]bool{}}
	}

	// Edges: every relationship target addressing a pending draft is a
	// dependency (referenced first); a target addressing nothing is a
	// pre-flight refusal unless the line already exists in the store.
	for _, node := range nodes {
		for _, field := range conformance.RelationshipFieldNames() {
			for _, raw := range node.artifact.Relations[field] {
				ref, perr := conformance.ParseReference(raw, node.artifact.Namespace, node.artifact.Type)
				if perr != nil {
					return nil, &BatchUnresolvedError{Draft: node.artifact.Type + ":" + node.artifact.ID, Target: raw, Detail: perr.Error()}
				}
				key := batchKey(ref.Namespace, ref.Type, ref.ID)
				if ref.Namespace == node.artifact.Namespace && ref.Type == node.artifact.Type && ref.ID == node.artifact.ID {
					// A self-reference is a length-1 cycle: the Kahn
					// pass below reports it with the cycle class.
					node.deps[key] = true
					continue
				}
				if _, pending := nodes[key]; pending {
					node.deps[key] = true
					continue
				}
				// Not pending: must already be published (line-level
				// existence; a versioned reference is covered by the
				// line check — exact-instance semantics stay Publish's).
				units, uerr := st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
				if uerr != nil {
					return nil, fmt.Errorf("publish: cannot check reference %q of draft %s against the store: %w",
						raw, node.artifact.Type+":"+node.artifact.ID, uerr)
				}
				if len(units) == 0 {
					return nil, &BatchUnresolvedError{Draft: node.artifact.Type + ":" + node.artifact.ID, Target: raw}
				}
			}
		}
	}

	order, err := batchTopoOrder(nodes)
	if err != nil {
		return nil, err
	}

	result := &PublishBatchResult{Published: []*PublishResult{}}
	for _, key := range order {
		node := nodes[key]
		// The target is the draft's own identity (unqualified; the
		// project hint scopes the lookup exactly like `eka publish`).
		res, err := Authoring.Publish(rt, node.artifact.Type+":"+node.artifact.ID, PublishOptions{Project: project})
		if err != nil {
			// Publish's error classes propagate unchanged (the caller
			// renders *PublishError reports); the result carries the
			// publishes completed before the failure.
			return result, err
		}
		result.Published = append(result.Published, res)
	}
	return result, nil
}

// batchNode is one pending draft in the batch publish graph: its draft
// file, its scanned artifact (the identity + relationships source of
// truth) and its dependencies (referenced pending drafts, keyed by
// identity).
type batchNode struct {
	draft    Draft
	artifact *conformance.Artifact
	deps     map[string]bool
}

// batchKey renders the deterministic identity key of one draft line:
// "<namespace>/<type>:<id>".
func batchKey(ns, typeToken, id string) string {
	return ns + "/" + typeToken + ":" + id
}

// batchTopoOrder orders the pending drafts so every draft is published
// after the drafts it references (Kahn's algorithm; the ready set is
// consumed in sorted identity order, so the order is byte-deterministic
// for a given pending set). A leftover set — drafts that can never
// become ready because a dependency cycle blocks them — is a
// *BatchCycleError naming the drafts, sorted deterministically.
func batchTopoOrder(nodes map[string]*batchNode) ([]string, error) {
	indegree := make(map[string]int, len(nodes))
	consumers := make(map[string][]string, len(nodes)) // key -> dependents
	for key, node := range nodes {
		indegree[key] = len(node.deps)
		for dep := range node.deps {
			consumers[dep] = append(consumers[dep], key)
		}
	}
	for _, dependents := range consumers {
		sort.Strings(dependents)
	}

	ready := make([]string, 0, len(nodes))
	for key, n := range indegree {
		if n == 0 {
			ready = append(ready, key)
		}
	}
	sort.Strings(ready)

	order := make([]string, 0, len(nodes))
	for len(ready) > 0 {
		key := ready[0]
		ready = ready[1:]
		order = append(order, key)
		for _, dependent := range consumers[key] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Strings(ready)
			}
		}
	}
	if len(order) != len(nodes) {
		leftover := make([]string, 0, len(nodes)-len(order))
		for key := range nodes {
			if indegree[key] > 0 {
				leftover = append(leftover, key)
			}
		}
		sort.Strings(leftover)
		drafts := make([]string, 0, len(leftover))
		for _, key := range leftover {
			node := nodes[key]
			drafts = append(drafts, node.artifact.Type+":"+node.artifact.ID)
		}
		sort.Strings(drafts)
		return nil, &BatchCycleError{Drafts: drafts}
	}
	return order, nil
}
