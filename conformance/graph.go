package conformance

import (
	"fmt"
	"sort"
	"strings"
)

// This file implements the graph-aware validation pass R13 (ADR-019 D6):
// the first cross-file validation of the engine. It runs after the
// per-file pass (R0-R12) and resolves `discusses` edges across the
// analyzed set:
//
//	- the cmt- note contract (ADR-019 D7): every note's structured
//	  content must satisfy its role schema (validateNoteContent);
//	- `discusses` resolution (ADR-019 §4.3): every discusses target
//	  must resolve — in repository mode within the analyzed set, in CKO
//	  mode through the runtime resolver;
//	- the work-item transition gates (ADR-019 D6): a work item at
//	  in-review requires at least one child note with role
//	  "implementation" and note-state "resolved"; a work item at done
//	  requires EVERY child note resolved (no open notes).
//
// Findings are R13 errors in deterministic order (sorted by the finding's
// file — the artifact's canonical form — then message). A note whose
// subject is NOT a work item is valid and simply ungated (the gates apply
// to work items only).

// GraphOptions configures one graph pass run.
type GraphOptions struct {
	// Resolve reports whether a parsed reference resolves outside the
	// analyzed set (CKO mode: the runtime store). Nil = resolve within
	// the set only (repository mode).
	Resolve func(ref Reference) bool
	// SkipGates skips the work-item transition gate checks. CKO-level
	// validation validates ONE unit (the unit being published): the
	// gate checks need the full authoring set and would false-positive
	// on a single work item, so publish keeps the note-contract and
	// discusses-resolution checks only — `eka sync` remains the source
	// of truth for the gates (ADR-019 D6).
	SkipGates bool
}

// ValidateGraph runs the R13 graph pass over the analyzed set and returns
// the deterministic findings. Call sites: the repository gate (Validate,
// after the per-file pass) and the CKO-level gate (ValidateCKO, with
// SkipGates — the unit being published is a single-artifact set).
func ValidateGraph(artifacts []*Artifact, opts ...GraphOptions) []Result {
	o := GraphOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	g := &graphPass{artifacts: artifacts, resolveFn: o.Resolve, skipGates: o.SkipGates}
	g.index()
	g.run()
	g.sortResults()
	return g.results
}

// graphPass carries the state of one graph pass run.
type graphPass struct {
	artifacts []*Artifact
	byLine    map[string][]*Artifact
	// resolveFn is the CKO-mode external resolver (nil in repository
	// mode: resolution uses the set index).
	resolveFn func(ref Reference) bool
	skipGates bool
	results   []Result
}

// index builds the identity-line index of the analyzed set (the same
// shape the per-file rules use).
func (g *graphPass) index() {
	g.byLine = make(map[string][]*Artifact)
	for _, a := range g.artifacts {
		key := identityKey(a.Namespace, a.Type, a.ID)
		g.byLine[key] = append(g.byLine[key], a)
	}
	for _, bucket := range g.byLine {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].InstanceVersion < bucket[j].InstanceVersion
		})
	}
}

// resolve returns the artifact a parsed reference points to within the
// analyzed set, or nil.
func (g *graphPass) resolve(ref reference) *Artifact {
	bucket := g.byLine[identityKey(ref.Namespace, ref.Type, ref.ID)]
	if len(bucket) == 0 {
		return nil
	}
	if ref.HasVersion {
		for _, a := range bucket {
			if a.InstanceVersion == ref.Version {
				return a
			}
		}
		return nil
	}
	return bucket[0]
}

// resolved reports whether a parsed reference resolves: in CKO mode
// through the external resolver (the runtime store), in repository mode
// within the set index.
func (g *graphPass) resolved(ref reference) bool {
	if g.resolveFn != nil {
		return g.resolveFn(Reference(ref))
	}
	return g.resolve(ref) != nil
}

// run executes the three checks of the pass.
func (g *graphPass) run() {
	for _, a := range g.artifacts {
		if a.Type != "cmt" {
			continue
		}
		// The note contract (D7) — role schema, per-role fields, strict
		// unknown-key rejection.
		g.results = append(g.results, validateNoteContent(a)...)
		// discusses resolution (ADR-019 §4.3): unresolvable targets are
		// errors. Malformed references are reported by R5.
		for _, raw := range a.Relations["discusses"] {
			ref, err := parseReference(raw, a.Namespace, a.Type)
			if err != nil {
				continue // R5 reports the malformed reference.
			}
			if !g.resolved(ref) {
				g.results = append(g.results, Result{
					File: a.RelPath, Rule: Rule13, Severity: SeverityError,
					Message: fmt.Sprintf("discusses target %q does not resolve", raw),
				})
			}
		}
		// replies-to resolution (ADR-019 D8 revised): a reply attaches
		// to exactly ONE parent cmt- note; the parent must resolve and
		// be a note line (replies to non-note targets are refused).
		for _, raw := range a.Relations["replies-to"] {
			ref, err := parseReference(raw, a.Namespace, a.Type)
			if err != nil {
				continue // R5 reports the malformed reference.
			}
			if ref.Type != "cmt" {
				g.results = append(g.results, Result{
					File: a.RelPath, Rule: Rule13, Severity: SeverityError,
					Message: fmt.Sprintf("replies-to target %q is not a note (cmt-) line; replies attach to one parent note", raw),
				})
				continue
			}
			if !g.resolved(ref) {
				g.results = append(g.results, Result{
					File: a.RelPath, Rule: Rule13, Severity: SeverityError,
					Message: fmt.Sprintf("replies-to target %q does not resolve", raw),
				})
			}
		}
		if len(a.Relations["replies-to"]) > 1 {
			g.results = append(g.results, Result{
				File: a.RelPath, Rule: Rule13, Severity: SeverityError,
				Message: fmt.Sprintf("replies-to allows exactly one parent note (got %d targets)", len(a.Relations["replies-to"])),
			})
		}
	}
	if g.skipGates {
		return
	}
	for _, a := range g.artifacts {
		if !workItemTypes[a.Type] {
			continue // The gates apply to work items only (D6).
		}
		state := a.States[DomainExecutionState]
		if state != "in-review" && state != "done" {
			continue
		}
		g.results = append(g.results, g.gateFindings(a, state)...)
	}
}

// gateFindings evaluates the D6 gates for one work item at the given
// execution state (its current state in the set, or the destination of a
// requested transition in the early check).
func (g *graphPass) gateFindings(a *Artifact, state string) []Result {
	notes := g.notesDiscussing(a)
	var findings []Result
	switch state {
	case "in-review":
		// in-progress -> in-review requires at least one child note
		// with role "implementation" and note-state "resolved".
		for _, n := range notes {
			role, _ := n.ContentFields["role"].(string)
			if role == NoteRoleImplementation && n.States[DomainNoteState] == "resolved" {
				return nil
			}
		}
		findings = append(findings, Result{
			File: a.RelPath, Rule: Rule13, Severity: SeverityError,
			Message: "transition gate R13: execution-state in-review requires at least one child note (discusses) with role \"implementation\" and note-state \"resolved\"",
		})
	case "done":
		// in-review -> done requires EVERY child note resolved.
		var open []string
		for _, n := range notes {
			if n.States[DomainNoteState] != "resolved" {
				open = append(open, noteIdentityForm(n))
			}
		}
		if len(open) > 0 {
			sort.Strings(open)
			findings = append(findings, Result{
				File: a.RelPath, Rule: Rule13, Severity: SeverityError,
				Message: fmt.Sprintf("transition gate R13: execution-state done requires every child note resolved; open notes: %s",
					strings.Join(open, ", ")),
			})
		}
	}
	return findings
}

// notesDiscussing returns the note artifacts of the set whose discusses
// field resolves to the given work item's identity (a line-level
// reference matches the line; a version-pinned reference matches the
// exact instance).
func (g *graphPass) notesDiscussing(a *Artifact) []*Artifact {
	var notes []*Artifact
	for _, b := range g.artifacts {
		if b == a || b.Type != "cmt" {
			continue
		}
		for _, raw := range b.Relations["discusses"] {
			ref, err := parseReference(raw, b.Namespace, b.Type)
			if err != nil {
				continue // R5 reports the malformed reference.
			}
			if ref.Namespace != a.Namespace || ref.Type != a.Type || ref.ID != a.ID {
				continue
			}
			if ref.HasVersion && ref.Version != a.InstanceVersion {
				continue
			}
			notes = append(notes, b)
			break
		}
	}
	return notes
}

// noteIdentityForm renders the line identity of a note for diagnostics
// ("<namespace>/<type>:<id>").
func noteIdentityForm(n *Artifact) string {
	return n.Namespace + "/" + n.Type + ":" + n.ID
}

// sortResults orders the findings deterministically: by file (the
// artifact's canonical form / relative path), then message.
func (g *graphPass) sortResults() {
	sort.Slice(g.results, func(i, j int) bool {
		a, b := g.results[i], g.results[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Message < b.Message
	})
}
