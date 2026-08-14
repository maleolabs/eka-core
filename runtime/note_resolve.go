package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the explicit note resolution and reply APIs
// (ADR-019 D8 revised): resolving a note is an EXPLICIT, documented
// action — never a silent file edit. `eka note reply` attaches a reply
// comment to exactly one parent note (single-parent replies), and
// `eka note resolve` marks a note resolved — optionally with a reply
// that documents the resolution. Both operate through the Authoring
// API (drafts and the publish pipeline), never direct store access.
//
// Resolution targets:
//   - a draft note: the draft file's note-state is advanced to
//     "resolved" with a change-log entry (the R13 gates see drafts, so
//     the resolved draft gate-satisfies immediately);
//   - a published note: the unit is immutable, so the resolution
//     scaffolds a resolved draft of the same line and publishes it
//     through the normal publish pipeline (a new instance).
//
// ResolveAllNotes resolves every open note discussing one subject unit
// (draft + published) — "resolve all in canonical scope".

// --- NoteReply ---

// NoteReplyRequest describes one reply draft to create.
type NoteReplyRequest struct {
	// RepoPath is the directory the repository is addressed from.
	RepoPath string
	// Parent is the replied-to note line: "<type>:<id>" (unqualified)
	// or "<ns>/<type>:<id>" (qualified, must equal the repository
	// namespace). The parent must be a cmt- line in the workspace
	// store OR a draft of the project.
	Parent string
	// By is the reply author identity (non-empty).
	By conformance.AuthorIdentity
	// Body is the reply body (non-empty; the reply content is
	// {role: "reply", body: <body>}).
	Body string
	// Domain optionally declares the reply's Engineering Domain
	// (contextable; "" = derived from cmt- = Execution).
	Domain string
}

// NoteReplyResult is the deterministic outcome of one reply draft.
type NoteReplyResult struct {
	// ID is the generated reply id ("<parent-id>-reply[-<n>]").
	ID string
	// Parent is the resolved parent identity ("<ns>/cmt:<id>").
	Parent string
	// Path is the absolute draft file path.
	Path string
}

// NoteReply creates one reply draft: a cmt- note with role "reply"
// ({role, body}) wired to its single parent through the replies-to
// relationship. The reply never carries its own discusses edge — its
// context is inherited from the parent — and therefore never satisfies
// a transition gate on its own (gates read discusses edges). Reply
// identity: "<parent-id>-reply[-<n>]" (lowest free suffix).
func (AuthoringService) NoteReply(rt *Runtime, req NoteReplyRequest) (*NoteReplyResult, error) {
	if strings.TrimSpace(req.By.Name) == "" {
		return nil, &NoteRefusal{
			Reason: "the reply authority (by) is required",
			Hint:   "pass --by <name> or let it resolve from `git config user.name`",
		}
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, fmt.Errorf("note: reply requires a non-empty body (--body or --content-file)") // Exit 2: usage.
	}
	root, meta, err := resolveRepoContext(req.RepoPath)
	if err != nil {
		var ctx repoContext
		if errors.As(err, &ctx) {
			return nil, &NoteRefusal{Reason: ctx.Error(), Hint: "run 'eka init' first"}
		}
		return nil, err
	}
	ref, err := conformance.ParseReference(req.Parent, "", "")
	if err != nil {
		return nil, fmt.Errorf("note: invalid parent %q: %w", req.Parent, err) // Exit 2: usage.
	}
	if ref.Type != "cmt" {
		return nil, fmt.Errorf("note: reply parent %q is not a note (cmt-) line; replies attach to one parent note", req.Parent) // Exit 2.
	}
	if ref.HasVersion {
		return nil, fmt.Errorf("note: %s is a canonical published form; replies attach to the parent's line", req.Parent) // Exit 2.
	}
	if ref.Namespace != "" && ref.Namespace != meta.Namespace {
		return nil, fmt.Errorf("note: parent namespace %s differs from the repository namespace %s", ref.Namespace, meta.Namespace) // Exit 2.
	}
	if ref.Namespace == "" {
		ref.Namespace = meta.Namespace
	}
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	repo, found, err := ws.FindRepo(root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &NoteRefusal{
			Reason: "the repository is not registered in the workspace",
			Hint:   "run 'eka sync' once to register the repository identity from eka.yaml",
		}
	}
	project := repo.ProjectID
	// The parent must resolve: a draft of the project or a published
	// cmt- unit of the line.
	if _, err := os.Stat(draftPath(ws, project, "cmt", ref.ID)); err != nil {
		st, serr := rt.requireStore()
		if serr != nil {
			return nil, serr
		}
		units, uerr := st.UnitsByLine(ref.Namespace, "cmt", ref.ID)
		if uerr != nil {
			return nil, fmt.Errorf("note: %w", uerr)
		}
		if len(units) == 0 {
			return nil, &NoteRefusal{
				Reason: fmt.Sprintf("reply parent %s/%s:%s was not found (no draft, no published unit)", ref.Namespace, ref.Type, ref.ID),
				Hint:   "reply to a note created by 'eka note' or a published cmt- note",
			}
		}
	}
	domain := ""
	if strings.TrimSpace(req.Domain) != "" {
		d, ok := conformance.ParseDomain(strings.TrimSpace(req.Domain))
		if !ok {
			return nil, fmt.Errorf("note: unknown engineering domain %q (allowed: %s)",
				req.Domain, strings.Join(conformance.DomainNames(), ", ")) // Exit 2: usage.
		}
		domain = string(d)
	}
	replyID := deriveReplyID(ws, project, meta.Namespace, ref.ID)
	parentForm := ref.Namespace + "/" + ref.Type + ":" + ref.ID
	content, err := json.Marshal(map[string]any{"role": conformance.NoteRoleReply, "body": body})
	if err != nil {
		return nil, fmt.Errorf("note: cannot encode the reply content: %w", err)
	}
	tmp, err := os.CreateTemp("", "eka-reply-*.json")
	if err != nil {
		return nil, fmt.Errorf("note: cannot stage the reply content: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note: cannot stage the reply content: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note: cannot stage the reply content: %w", err)
	}
	defer os.Remove(tmpPath)
	draft, err := Authoring.NewDraft(rt, NewDraftRequest{
		Project:       project,
		Namespace:     meta.Namespace,
		Type:          "cmt",
		ID:            replyID,
		Domain:        domain,
		By:            req.By,
		Relationships: []exchange.Relationship{{Type: "replies-to", Target: parentForm}},
		ContentFile:   tmpPath,
	})
	if err != nil {
		return nil, fmt.Errorf("note: %w", err)
	}
	return &NoteReplyResult{ID: replyID, Parent: parentForm, Path: draft.Path}, nil
}

// deriveReplyID computes the deterministic reply id:
// "<parent-id>-reply", with the lowest free numeric suffix when taken.
func deriveReplyID(ws *workspace.Workspace, project, ns, parentID string) string {
	base := parentID + "-reply"
	st := ws.Store()
	taken := func(id string) bool {
		if _, err := os.Stat(draftPath(ws, project, "cmt", id)); err == nil {
			return true
		}
		units, err := st.UnitsByLine(ns, "cmt", id)
		if err != nil {
			return true // Conservative: treat store failures as taken.
		}
		return len(units) > 0
	}
	if !taken(base) {
		return base
	}
	for n := 2; ; n++ {
		candidate := fmt.Sprintf("%s-%d", base, n)
		if !taken(candidate) {
			return candidate
		}
	}
}

// --- Note resolve ---

// ResolveNoteRequest describes one explicit note resolution.
type ResolveNoteRequest struct {
	// RepoPath is the directory the repository is addressed from.
	RepoPath string
	// Target is the note line to resolve: "<type>:<id>" (unqualified)
	// or "<ns>/<type>:<id>" (qualified, must equal the repository
	// namespace). Must be a cmt- line with a draft or a published
	// unit.
	Target string
	// By is the resolution authority identity (non-empty).
	By conformance.AuthorIdentity
	// ReplyBody optionally documents the resolution: a reply note
	// (role reply) is attached to the target before it is resolved.
	// "" = status-only resolution.
	ReplyBody string
}

// ResolveNoteResult is the deterministic outcome of one resolution.
type ResolveNoteResult struct {
	// Target is the resolved note identity ("<ns>/cmt:<id>").
	Target string
	// Path is the updated draft file path (draft case) or the
	// published result path ("" when the note has no draft file).
	Path string
	// Published reports a published-instance resolution (the unit is
	// immutable; a new instance now carries note-state resolved).
	Published bool
	// AlreadyResolved reports a no-op (the note was already resolved).
	AlreadyResolved bool
	// ReplyID is the optional attached reply draft id.
	ReplyID string
}

// ResolveNote explicitly resolves ONE note line. The resolution is
// recorded in the note's change log (note-state open -> resolved, with
// the authority identity); an optional reply documents the resolution
// before the status change. Drafts are updated in place; published
// units (immutable) are resolved through the publish pipeline — a new
// instance of the line. A note already resolved reports
// AlreadyResolved (no error).
func (AuthoringService) ResolveNote(rt *Runtime, req ResolveNoteRequest) (*ResolveNoteResult, error) {
	if strings.TrimSpace(req.By.Name) == "" {
		return nil, &NoteRefusal{
			Reason: "the resolution authority (by) is required",
			Hint:   "pass --by <name> or let it resolve from `git config user.name`",
		}
	}
	root, meta, err := resolveRepoContext(req.RepoPath)
	if err != nil {
		var ctx repoContext
		if errors.As(err, &ctx) {
			return nil, &NoteRefusal{Reason: ctx.Error(), Hint: "run 'eka init' first"}
		}
		return nil, err
	}
	ref, err := conformance.ParseReference(req.Target, "", "")
	if err != nil {
		return nil, fmt.Errorf("note resolve: invalid target %q: %w", req.Target, err) // Exit 2: usage.
	}
	if ref.Type != "cmt" {
		return nil, fmt.Errorf("note resolve: %q is not a note (cmt-) line; only notes are resolved", req.Target) // Exit 2.
	}
	if ref.HasVersion {
		return nil, fmt.Errorf("note resolve: %s is a canonical published form; resolve the note's line", req.Target) // Exit 2.
	}
	if ref.Namespace != "" && ref.Namespace != meta.Namespace {
		return nil, fmt.Errorf("note resolve: target namespace %s differs from the repository namespace %s", ref.Namespace, meta.Namespace) // Exit 2.
	}
	if ref.Namespace == "" {
		ref.Namespace = meta.Namespace
	}
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	repo, found, err := ws.FindRepo(root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &NoteRefusal{
			Reason: "the repository is not registered in the workspace",
			Hint:   "run 'eka sync' once to register the repository identity from eka.yaml",
		}
	}
	project := repo.ProjectID

	// The optional reply documenting the resolution.
	replyID := ""
	if strings.TrimSpace(req.ReplyBody) != "" {
		res, rerr := Authoring.NoteReply(rt, NoteReplyRequest{
			RepoPath: req.RepoPath,
			Parent:   req.Target,
			By:       req.By,
			Body:     req.ReplyBody,
		})
		if rerr != nil {
			return nil, rerr
		}
		replyID = res.ID
	}

	target := ref.Namespace + "/cmt:" + ref.ID
	// A note line may carry BOTH a draft and published instances (the
	// draft survives its publish): the resolution covers the whole
	// line — the draft is advanced in place and an open published
	// unit is advanced through the publish pipeline. A note with
	// neither is refused.
	draftFile := draftPath(ws, project, "cmt", ref.ID)
	_, draftErr := os.Stat(draftFile)
	draftExists := draftErr == nil
	res := &ResolveNoteResult{Target: target, ReplyID: replyID}
	if draftExists {
		draftResolved, rerr := resolveDraftNote(draftFile, req.By)
		if rerr != nil {
			return nil, rerr
		}
		res.Path = draftFile
		res.AlreadyResolved = draftResolved
	}
	st, serr := rt.requireStore()
	if serr != nil {
		return nil, serr
	}
	units, uerr := st.UnitsByLine(ref.Namespace, "cmt", ref.ID)
	if uerr != nil {
		return nil, fmt.Errorf("note resolve: %w", uerr)
	}
	publishedOpen := false
	for _, u := range units {
		if u.StateVector.NoteState != "resolved" {
			publishedOpen = true
			break
		}
	}
	if publishedOpen {
		published, perr := resolvePublishedNote(rt, ws, project, meta.Namespace, ref.ID, req.By)
		if perr != nil {
			var refusal *NoteRefusal
			if errors.As(perr, &refusal) {
				return nil, refusal
			}
			return nil, perr
		}
		res.Published = true
		res.AlreadyResolved = false
		if published.Path != "" && res.Path == "" {
			res.Path = published.Path
		}
	}
	if !draftExists && len(units) == 0 {
		return nil, &NoteRefusal{
			Reason: fmt.Sprintf("note %s was not found (no draft, no published unit)", target),
			Hint:   "create it with 'eka note <target> --role <role>' first",
		}
	}
	return res, nil
}

// resolveDraftNote advances a draft note's note-state to "resolved":
// the state block and the change log (open -> resolved, with the
// authority). Returns true when the note was already resolved (the
// file is left untouched).
func resolveDraftNote(path string, by conformance.AuthorIdentity) (alreadyResolved bool, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("note resolve: cannot read draft %s: %w", path, err)
	}
	var doc draftDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return false, fmt.Errorf("note resolve: draft %s is not valid JSON: %v", path, err)
	}
	if doc.State == nil {
		doc.State = map[string]string{}
	}
	if doc.State["noteState"] == "resolved" {
		return true, nil
	}
	doc.State["noteState"] = "resolved"
	doc.ChangeLog = append(doc.ChangeLog, draftChangeLog{
		Date:   time.Now().Format("2006-01-02"),
		Domain: conformance.StateKeyCamel(conformance.DomainNoteState),
		From:   "open",
		To:     "resolved",
		By:     by,
	})
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, fmt.Errorf("note resolve: cannot encode draft %s: %w", path, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return false, fmt.Errorf("note resolve: cannot write draft %s: %w", path, err)
	}
	return false, nil
}

// resolvePublishedNote resolves a published (immutable) note line: the
// current payload is scaffolded as a resolved draft of the same line
// (author and change log carried over, note-state resolved) and
// published through the normal pipeline — a new instance.
func resolvePublishedNote(rt *Runtime, ws *workspace.Workspace, project, ns, id string, by conformance.AuthorIdentity) (*ResolveNoteResult, error) {
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	units, err := st.UnitsByLine(ns, "cmt", id)
	if err != nil {
		return nil, fmt.Errorf("note resolve: %w", err)
	}
	if len(units) == 0 {
		return nil, &NoteRefusal{
			Reason: fmt.Sprintf("note %s/cmt:%s was not found (no draft, no published unit)", ns, id),
			Hint:   "create it with 'eka note <target> --role <role>' first",
		}
	}
	u := units[0]
	if u.StateVector.NoteState == "resolved" {
		return &ResolveNoteResult{AlreadyResolved: true}, nil
	}
	// Scaffold the resolved draft: the current payload, the carried
	// relationships, the authority, and note-state resolved.
	tmp, err := os.CreateTemp("", "eka-resolve-*.json")
	if err != nil {
		return nil, fmt.Errorf("note resolve: cannot stage the note content: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(u.ContentPayload); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note resolve: cannot stage the note content: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note resolve: cannot stage the note content: %w", err)
	}
	defer os.Remove(tmpPath)
	draft, err := Authoring.NewDraft(rt, NewDraftRequest{
		Project:       project,
		Namespace:     ns,
		Type:          "cmt",
		ID:            id,
		By:            by,
		Relationships: u.Relationships,
		ContentFile:   tmpPath,
	})
	if err != nil {
		return nil, fmt.Errorf("note resolve: %w", err)
	}
	if _, err := resolveDraftNote(draft.Path, by); err != nil {
		return nil, err
	}
	if _, err := Authoring.Publish(rt, "cmt:"+id, PublishOptions{}); err != nil {
		return nil, fmt.Errorf("note resolve: %w", err)
	}
	return &ResolveNoteResult{Target: ns + "/cmt:" + id, Path: draft.Path, Published: true}, nil
}

// ResolveAllNotesRequest describes a resolution of EVERY open note in
// the canonical scope of one subject unit.
type ResolveAllNotesRequest struct {
	// RepoPath is the directory the repository is addressed from.
	RepoPath string
	// Subject is the work item (or any artifact) line whose discussing
	// notes are resolved: "<type>:<id>" or "<ns>/<type>:<id>".
	Subject string
	// By is the resolution authority identity.
	By conformance.AuthorIdentity
	// ReplyBody optionally documents the resolution (attached to each
	// resolved note before its status change).
	ReplyBody string
}

// ResolveAllNotesResult is the deterministic outcome of a batch
// resolution.
type ResolveAllNotesResult struct {
	// Resolved lists the resolved note identities.
	Resolved []string
	// AlreadyResolved lists the notes that were already resolved.
	AlreadyResolved []string
	// Replies lists the reply draft ids attached during the run.
	Replies []string
}

// ResolveAllNotes resolves every OPEN note discussing the subject unit
// — draft notes (updated in place) and published notes (new instances
// through the publish pipeline). Notes already resolved are reported,
// not re-resolved. Deterministic order (canonical identity).
func (AuthoringService) ResolveAllNotes(rt *Runtime, req ResolveAllNotesRequest) (*ResolveAllNotesResult, error) {
	if strings.TrimSpace(req.By.Name) == "" {
		return nil, &NoteRefusal{
			Reason: "the resolution authority (by) is required",
			Hint:   "pass --by <name> or let it resolve from `git config user.name`",
		}
	}
	root, meta, err := resolveRepoContext(req.RepoPath)
	if err != nil {
		var ctx repoContext
		if errors.As(err, &ctx) {
			return nil, &NoteRefusal{Reason: ctx.Error(), Hint: "run 'eka init' first"}
		}
		return nil, err
	}
	ref, err := conformance.ParseReference(req.Subject, "", "")
	if err != nil {
		return nil, fmt.Errorf("note resolve: invalid subject %q: %w", req.Subject, err) // Exit 2.
	}
	if ref.Namespace != "" && ref.Namespace != meta.Namespace {
		return nil, fmt.Errorf("note resolve: subject namespace %s differs from the repository namespace %s", ref.Namespace, meta.Namespace) // Exit 2.
	}
	if ref.Namespace == "" {
		ref.Namespace = meta.Namespace
	}
	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	repo, found, err := ws.FindRepo(root)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, &NoteRefusal{
			Reason: "the repository is not registered in the workspace",
			Hint:   "run 'eka sync' once to register the repository identity from eka.yaml",
		}
	}
	project := repo.ProjectID
	notes, err := collectNotesForSubject(rt, ws, project, ref)
	if err != nil {
		return nil, err
	}
	var out ResolveAllNotesResult
	for _, n := range notes {
		res, err := Authoring.ResolveNote(rt, ResolveNoteRequest{
			RepoPath:  req.RepoPath,
			Target:    n,
			By:        req.By,
			ReplyBody: req.ReplyBody,
		})
		if err != nil {
			return nil, fmt.Errorf("note resolve: %s: %w", n, err)
		}
		if res.AlreadyResolved {
			out.AlreadyResolved = append(out.AlreadyResolved, n)
		} else {
			out.Resolved = append(out.Resolved, n)
		}
		if res.ReplyID != "" {
			out.Replies = append(out.Replies, res.ReplyID)
		}
	}
	return &out, nil
}

// collectNotesForSubject gathers the identities of every cmt- note
// (draft + published) discussing the subject line, sorted by canonical
// identity — the canonical scope of a batch resolution.
func collectNotesForSubject(rt *Runtime, ws *workspace.Workspace, project string, subject conformance.Reference) ([]string, error) {
	seen := map[string]bool{}
	var ids []string
	// Published units.
	st, err := rt.requireStore()
	if err != nil {
		return nil, err
	}
	units, err := st.UnitsByProject(project)
	if err != nil {
		return nil, fmt.Errorf("note resolve: %w", err)
	}
	for _, u := range units {
		if u.Identity.Type != "cmt" {
			continue
		}
		if !discussesTarget(u, subject) {
			continue
		}
		key := u.Identity.Namespace + "/cmt:" + u.Identity.ID
		if !seen[key] {
			seen[key] = true
			ids = append(ids, key)
		}
	}
	// Drafts.
	dir := filepath.Join(draftsRoot(ws), project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Continue with the published set.
		} else {
			return nil, fmt.Errorf("note resolve: cannot scan drafts: %w", err)
		}
	} else {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || !strings.HasPrefix(e.Name(), "cmt-") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			artifact, aerr := conformance.ScanFile(path)
			if aerr != nil || artifact == nil || artifact.Type != "cmt" {
				continue
			}
			if !discussesTargetArtifact(artifact, subject) {
				continue
			}
			key := artifact.Namespace + "/cmt:" + artifact.ID
			if !seen[key] {
				seen[key] = true
				ids = append(ids, key)
			}
		}
	}
	sort.Strings(ids)
	return ids, nil
}
