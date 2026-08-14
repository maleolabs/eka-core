package view

import (
	"sort"
	"strings"

	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the note accessors of the knowledge graph
// (ADR-019 D8 revised): cmt- notes are wired to their subject through
// the `discusses` relationship, and the ticket projection can surface
// them (eka view ticket --with-note / --with-comments).
//
// The discussed target is stored in its resolved qualified form — the
// line form ("<namespace>/<type>:<id>") or the canonical form with the
// instance version appended ("<namespace>/<type>:<id>:<v>") — and
// matched against the subject's line form, so a note addresses its
// subject's LINE (all instances), not one payload.

// NotesFor returns the cmt- notes discussing ANY of the given subject
// line forms ("<namespace>/<type>:<id>"), deduplicated by canonical
// form and sorted by canonical identity — deterministic. The highest
// instance of each note line is returned (the line's CURRENT payload —
// the latest instance; older instances stay in the archive).
//
// RepliesFor returns the cmt- notes attached to ONE parent note line
// through the replies-to relationship (single-parent replies, ADR-019
// D8 revised), deduplicated by canonical form and sorted by canonical
// identity — the reply tree of one parent.
func (g *Graph) NotesFor(subjects ...string) []*exchange.Unit {
	want := make(map[string]bool, len(subjects))
	for _, s := range subjects {
		if s != "" {
			want[s] = true
		}
	}
	// Every matching instance is collected, then per note line the
	// line's CURRENT payload (the latest instance) wins — older
	// instances stay in the archive and never surface here.
	latest := map[string]*exchange.Unit{}
	for _, u := range g.units {
		if u.Identity.Type != "cmt" {
			continue
		}
		discusses := false
		for _, r := range u.Relationships {
			if r.Type != "discusses" {
				continue
			}
			if want[r.Target] || want[subjectLineForm(r.Target)] {
				discusses = true
				break
			}
		}
		if !discusses {
			continue
		}
		key := LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
		if prev, ok := latest[key]; !ok || u.Identity.InstanceVersion > prev.Identity.InstanceVersion {
			latest[key] = u
		}
	}
	notes := make([]*exchange.Unit, 0, len(latest))
	for _, u := range latest {
		notes = append(notes, u)
	}
	sort.Slice(notes, func(i, j int) bool {
		return notes[i].CanonicalIdentityForm < notes[j].CanonicalIdentityForm
	})
	return notes
}

// subjectLineForm normalizes a stored discusses target to its line
// form: "<namespace>/<type>:<id>" — the optional instance-version
// suffix (":<digits>") is dropped. Targets that are already line forms
// pass through unchanged.
func subjectLineForm(target string) string {
	if i := strings.LastIndex(target, ":"); i >= 0 {
		suffix := target[i+1:]
		if suffix != "" && allDigits(suffix) {
			return target[:i]
		}
	}
	return target
}

// allDigits reports whether s is non-empty and consists of digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// RepliesFor returns the reply notes whose replies-to relationship
// resolves to the given parent line form. One reply per note line —
// the line's CURRENT payload (the latest instance).
func (g *Graph) RepliesFor(parentForm string) []*exchange.Unit {
	var replies []*exchange.Unit
	seen := map[string]*exchange.Unit{}
	for _, u := range g.units {
		if u.Identity.Type != "cmt" {
			continue
		}
		for _, r := range u.Relationships {
			if r.Type != "replies-to" {
				continue
			}
			if r.Target != parentForm && subjectLineForm(r.Target) != parentForm {
				continue
			}
			key := LineForm(u.Identity.Namespace, u.Identity.Type, u.Identity.ID)
			prev, ok := seen[key]
			if ok {
				if u.Identity.InstanceVersion <= prev.Identity.InstanceVersion {
					break // keep the line's current (latest) payload
				}
			}
			seen[key] = u
			replies = append(replies, u)
			break
		}
	}
	sort.Slice(replies, func(i, j int) bool {
		return replies[i].CanonicalIdentityForm < replies[j].CanonicalIdentityForm
	})
	return replies
}

// TicketNote is one note of the ticket projection: the note unit plus
// its single-level replies (each reply is a cmt- unit whose
// replies-to relationship resolves to the note's line).
type TicketNote struct {
	Note    *exchange.Unit
	Replies []*exchange.Unit
}
