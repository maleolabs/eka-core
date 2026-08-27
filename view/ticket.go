package view

import (
	"encoding/json"
	"fmt"

	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the ticket projection: one ticket (tkt-) with
// its projected status.
//
// Derivation:
//   - Target: a ticket identity, required. The target accepts a bare
//     ticket id, "tkt-<id>" or "tkt:<id>".
//   - Projected status: DERIVED from the referenced work item's
//     execution-state — the projection semantics read the owner state,
//     never the ticket's own `## Projected Status` content. A ticket
//     without a resolvable work item is a valid projection with the
//     explicit status "unresolved".
//   - Container: the ticket's derives-from ctr- reference, when it
//     resolves.
//   - References: the ticket's derives-from relationship targets in
//     stored (type, target) order, rendered in the authoring reference
//     convention.

// TicketProjection is the view over one ticket.
type TicketProjection struct {
	// Ticket is the projected ticket.
	Ticket Ticket
	// Container is the container the ticket derives from, or nil when
	// the ctr- reference does not resolve.
	Container *Container
	// WorkItem is the work item the ticket derives from, or nil when
	// the ticket has no resolvable work item reference.
	WorkItem *WorkItem
	// Projected is the ticket's projected status: the referenced work
	// item's execution-state, or "unresolved".
	Projected string
	// References are the derives-from relationship targets in stored
	// (type, target) order, rendered in the authoring reference
	// convention.
	References []string
	// Notes are the cmt- notes discussing the ticket or its referenced
	// work item (highest instance per note line, sorted by canonical
	// identity), each carrying its single-level replies (ADR-019 D8
	// revised). Additive: always projected, surfaced by the renderers
	// only when requested (eka view ticket --with-note /
	// --with-comments, eka get ticket --with-notes).
	Notes []TicketNote
	// ReviewSummary is the per-reviewer verdict summary of the review
	// trail (phase2-core-view): one entry per reviewer author with the
	// list of per-note verdicts — never a single aggregate. Additive,
	// schema-stable (eka-ticket-v1 reviewSummary); empty when no review
	// notes exist.
	ReviewSummary []ReviewerVerdicts `json:"reviewSummary,omitempty"`
}

// ReviewerVerdicts is one reviewer's slice of the review trail: the
// author identity name and every verdict that reviewer recorded, in
// canonical note order. Verdicts are advisory; composition is a list,
// never an aggregate.
type ReviewerVerdicts struct {
	Author   string   `json:"author"`
	Verdicts []string `json:"verdicts"`
}

// Name returns the registry name of the projection.
func (p *TicketProjection) Name() string { return "ticket" }

func buildTicket(g *Graph, target string) (Projection, error) {
	if target == "" {
		return nil, fmt.Errorf("the ticket projection requires a target: eka view ticket <tkt-id>")
	}
	item := g.TicketByTarget(target)
	if item == nil {
		return nil, &TargetNotFoundError{
			Projection: "ticket",
			Target:     target,
			Available:  g.TicketIDs(),
		}
	}
	p := &TicketProjection{
		Ticket: Ticket{
			Identity: LineForm(item.Identity.Namespace, item.Identity.Type, item.Identity.ID),
			Type:     item.Identity.Type,
			ID:       item.Identity.ID,
		},
		References: g.relsOf(item, "derives-from"),
	}
	if item.Identity.Type == "tkt" {
		// A ticket: the projected status derives from its referenced
		// work item's execution state.
		p.Container, p.WorkItem = g.ticketTargets(item)
		if p.WorkItem != nil {
			p.Projected = p.WorkItem.State
		} else {
			p.Projected = "unresolved"
		}
	} else {
		// A direct work item (sto-/ts-/bug-/td-/ch-/spk-): the
		// projected status IS its own execution state.
		container, _ := g.ticketTargets(item)
		p.Container = container
		p.WorkItem = g.workItemFor(item)
		p.Projected = p.WorkItem.State
	}
	// Notes: the cmt- notes discussing the ticket and, for a ticket,
	// its referenced work item (the items related to the ticket). For
	// a direct work item, the notes discussing the item itself. Each
	// note carries its single-level replies (replies-to tree).
	subjects := []string{LineForm(item.Identity.Namespace, item.Identity.Type, item.Identity.ID)}
	if p.WorkItem != nil {
		subjects = append(subjects, p.WorkItem.Identity)
	}
	parents := g.NotesFor(subjects...)
	p.Notes = make([]TicketNote, 0, len(parents))
	for _, n := range parents {
		p.Notes = append(p.Notes, TicketNote{
			Note:    n,
			Replies: g.RepliesFor(LineForm(n.Identity.Namespace, n.Identity.Type, n.Identity.ID)),
		})
	}
	p.ReviewSummary = buildReviewSummary(p.Notes)
	p.Ticket.Projected = p.Projected
	return p, nil
}

// buildReviewSummary groups review-role notes by author and lists each
// reviewer's verdicts in canonical order (phase2-core-view). Notes
// without a decodable role or without a verdict are skipped; reviewers
// appear in first-appearance order of their notes.
func buildReviewSummary(notes []TicketNote) []ReviewerVerdicts {
	var out []ReviewerVerdicts
	index := map[string]int{}
	for _, tn := range notes {
		content := map[string]any{}
		if tn.Note.Content.Representation == exchange.StructuredJSON {
			_ = json.Unmarshal(tn.Note.ContentPayload, &content)
		}
		if role, _ := content["role"].(string); role != "review" {
			continue
		}
		author := tn.Note.Author.Name
		if author == "" {
			continue
		}
		verdict, _ := content["verdict"].(string)
		if verdict == "" {
			continue
		}
		i, ok := index[author]
		if !ok {
			i = len(out)
			index[author] = i
			out = append(out, ReviewerVerdicts{Author: author})
		}
		out[i].Verdicts = append(out[i].Verdicts, verdict)
	}
	return out
}
