package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the `eka note` side of the Authoring API (ADR-019
// D8, revised per implementation feedback): NoteDraft creates one cmt-
// note as a DRAFT under EKA_HOME/drafts — the repository docs tree is
// the legacy backward-compatibility authoring path, not the target of
// current EKA authoring. The draft carries the `discusses` relationship
// to the subject target and the role content (ADR-019 D7); the caller
// publishes it (`eka publish`) when the note becomes evidence — until
// then the draft is already visible to the R13 transition gates, so a
// note can be scaffolded, edited to `note-state: resolved` and
// immediately gate-satisfying.
//
// Note identity: the note id is derived deterministically from the
// subject id and the role — "<subject-id>-<role>", with a numeric
// suffix ("-<n>") when that identity is already taken by a published
// unit or a draft. The `discusses` target is stored in its resolved
// qualified form ("<namespace>/<type>:<id>").
//
// `by` is an audit record, not an access control (D2).

// NoteDraftRequest describes one note draft to create.
type NoteDraftRequest struct {
	// RepoPath is the directory the repository is addressed from (the
	// walk-up locates the eka.yaml repository root — ADR-018).
	RepoPath string
	// Target is the note's subject: "<ns>/<type>:<id>" (qualified —
	// must equal the repository namespace) or "<type>:<id>"
	// (unqualified — the repository namespace applies). The subject
	// must resolve to a unit of the workspace store OR a draft of the
	// same project (draft tolerance: notes record evidence before the
	// subject is approved).
	Target string
	// Role is one of implementation | review | fix (ADR-019 D7).
	Role string
	// Domain is the optional declared Engineering Domain of the note
	// (contextable: any of the five canonical domains — architecture,
	// discovery, execution, operations, planning — accepted as the
	// canonical name or the lowercase query token, case-insensitive).
	// "" leaves the domain derived from the cmt- type token (Execution).
	Domain string
	// By is the draft author identity (non-empty; resolved by
	// BySource — the draft's author AND change-log authority).
	By conformance.AuthorIdentity
	// ContentFile optionally supplies the note content as a JSON object
	// (merged over the per-role empty template). "" scaffolds the empty
	// per-role template.
	ContentFile string
}

// NoteDraftResult is the deterministic outcome of one note draft.
type NoteDraftResult struct {
	// ID is the generated note id ("<subject-id>-<role>[-<n>]").
	ID string
	// Target is the resolved subject identity ("<namespace>/<type>:<id>").
	Target string
	// SubjectState reports how the subject resolved: "" (a unit of the
	// workspace store) or "draft" (draft tolerance — a draft of the
	// same project; evidence recorded before the subject is approved).
	SubjectState string
	// Path is the absolute draft file path.
	Path string
	// By is the draft author identity.
	By conformance.AuthorIdentity
}

// NoteRefusal is a deterministic note refusal carrying the user-facing
// reason and hint (exit 1 class: unresolvable subject, repository/
// workspace state).
type NoteRefusal struct {
	Reason string
	Hint   string
}

// Error renders the deterministic refusal message.
func (e *NoteRefusal) Error() string {
	return fmt.Sprintf("note refused: %s; %s", e.Reason, e.Hint)
}

// noteRoleTemplates are the empty per-role content templates (ADR-019
// D7): the scaffolded key set of every role. --content-file merges over
// them; the `role` field always ends up equal to the --role flag.
var noteRoleTemplates = map[string]map[string]any{
	conformance.NoteRoleImplementation: {
		"role":    conformance.NoteRoleImplementation,
		"summary": "",
		"changes": []string{},
		"tests":   []string{},
	},
	conformance.NoteRoleReview: {
		"role":    conformance.NoteRoleReview,
		"verdict": "",
		"notes":   []string{},
	},
	conformance.NoteRoleFix: {
		"role":      conformance.NoteRoleFix,
		"addresses": []string{},
		"detail":    "",
	},
}

// NoteDraft creates one cmt- note draft. Pipeline (deterministic):
//
//  1. resolve the repository context (eka.yaml walk-up; ADR-018) and
//     the workspace project (unregistered -> refusal);
//  2. resolve the subject target in the workspace store OR as a draft
//     of the same project (draft tolerance: notes record evidence
//     before the subject is approved; neither -> refusal with the
//     sync hint);
//  3. build the note content: the per-role empty template merged with
//     the --content-file JSON object, role forced to the flag value;
//  4. derive the note id ("<subject-id>-<role>[-<n>]", lowest free);
//  5. scaffold the cmt- draft (NewDraft: discusses wired to the
//     resolved subject, note-state initial "open"). The draft is NOT
//     published — `eka publish` persists it when it becomes evidence.
func (AuthoringService) NoteDraft(rt *Runtime, req NoteDraftRequest) (*NoteDraftResult, error) {
	if strings.TrimSpace(req.By.Name) == "" {
		return nil, &NoteRefusal{
			Reason: "the change-log authority (by) is required",
			Hint:   "pass --by <name> or let it resolve from `git config user.name`",
		}
	}
	role := strings.TrimSpace(req.Role)
	if _, ok := noteRoleTemplates[role]; !ok {
		return nil, fmt.Errorf("note: unknown role %q (allowed: implementation, review, fix)", role) // Exit 2: usage.
	}
	// The Engineering Domain of the note is contextable (ADR-019 D8
	// revised): any of the five canonical domains. The accepted spelling
	// is the canonical name or the lowercase query token,
	// case-insensitive; the stored value is the canonical name.
	domain := ""
	if strings.TrimSpace(req.Domain) != "" {
		d, ok := conformance.ParseDomain(strings.TrimSpace(req.Domain))
		if !ok {
			return nil, fmt.Errorf("note: unknown engineering domain %q (allowed: %s)",
				req.Domain, strings.Join(conformance.DomainNames(), ", ")) // Exit 2: usage.
		}
		domain = string(d)
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
		return nil, fmt.Errorf("note: invalid target %q: %w", req.Target, err) // Exit 2: usage.
	}
	if ref.HasVersion {
		return nil, fmt.Errorf("note: %s is a canonical published form; notes address the subject line", req.Target) // Exit 2.
	}
	if ref.Namespace != "" && ref.Namespace != meta.Namespace {
		return nil, fmt.Errorf("note: target namespace %s differs from the repository namespace %s; cross-platform access is read-only",
			ref.Namespace, meta.Namespace) // Exit 2: usage.
	}
	if ref.Namespace == "" {
		ref.Namespace = meta.Namespace
	}

	ws, err := rt.requireWorkspace()
	if err != nil {
		return nil, err
	}
	st, err := rt.requireStore()
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

	// The subject must resolve in the workspace store (ADR-019 §4.3:
	// unresolvable target -> error) OR as a draft of the same project
	// (draft tolerance: the skill records evidence BEFORE approval, so
	// the subject is still a draft when the note is created). The
	// discusses edge to a draft subject is allowed while the note's
	// content-state is draft (rule 5: unresolved reference = warning),
	// so the note can be scaffolded and published once the subject is.
	line, err := st.UnitsByLine(ref.Namespace, ref.Type, ref.ID)
	if err != nil {
		return nil, fmt.Errorf("note: %w", err)
	}
	subjectState := ""
	if len(line) == 0 {
		if _, err := os.Stat(draftPath(ws, project, ref.Type, ref.ID)); err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("note: cannot inspect the subject draft: %w", err)
			}
			return nil, &NoteRefusal{
				Reason: fmt.Sprintf("subject %s/%s:%s was not found in the workspace store", ref.Namespace, ref.Type, ref.ID),
				Hint:   "run 'eka sync' first (the docs tree is legacy authoring; notes attach to workspace knowledge)",
			}
		}
		// Draft tolerance: the subject exists as a draft of this
		// project, not yet in the store. The note is created against
		// the subject line; the caller reports the draft resolution.
		subjectState = "draft"
	}

	// Note content: the per-role empty template merged with the
	// --content-file JSON object; the role field is forced to the flag
	// value (the content can never disagree with --role).
	content := map[string]any{}
	for k, v := range noteRoleTemplates[role] {
		content[k] = v
	}
	if req.ContentFile != "" {
		fileData, err := os.ReadFile(req.ContentFile)
		if err != nil {
			return nil, fmt.Errorf("note: cannot read content file %s: %w", req.ContentFile, err)
		}
		var merge map[string]any
		if err := json.Unmarshal(fileData, &merge); err != nil {
			return nil, fmt.Errorf("note: content file %s is not a valid JSON object: %v", req.ContentFile, err)
		}
		for k, v := range merge {
			content[k] = v
		}
	}
	content["role"] = role

	noteID := deriveNoteID(ws, project, meta.Namespace, ref.ID, role)
	subjectForm := ref.Namespace + "/" + ref.Type + ":" + ref.ID

	// The content file of the draft: a temporary JSON object (the
	// NewDraft merge path — ContentFile). Removed after the scaffold.
	tmp, err := os.CreateTemp("", "eka-note-*.json")
	if err != nil {
		return nil, fmt.Errorf("note: cannot stage the note content: %w", err)
	}
	tmpPath := tmp.Name()
	contentJSON, err := json.Marshal(content)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note: cannot encode the note content: %w", err)
	}
	if _, err := tmp.Write(contentJSON); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note: cannot stage the note content: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("note: cannot stage the note content: %w", err)
	}
	defer os.Remove(tmpPath)

	draft, err := Authoring.NewDraft(rt, NewDraftRequest{
		Project:       project,
		Namespace:     meta.Namespace,
		Type:          "cmt",
		ID:            noteID,
		Domain:        domain,
		By:            req.By,
		Relationships: []exchange.Relationship{{Type: "discusses", Target: subjectForm}},
		ContentFile:   tmpPath,
	})
	if err != nil {
		return nil, fmt.Errorf("note: %w", err)
	}
	return &NoteDraftResult{
		ID:           noteID,
		Target:       subjectForm,
		SubjectState: subjectState,
		Path:         draft.Path,
		By:           req.By,
	}, nil
}

// deriveNoteID computes the deterministic note id: "<subject-id>-<role>",
// with the lowest free numeric suffix ("-2", "-3", ...) when that
// identity is already taken by a published cmt- unit of the line or by a
// draft. Deterministic for a given store state.
func deriveNoteID(ws *workspace.Workspace, project, ns, subjectID, role string) string {
	base := subjectID + "-" + role
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
