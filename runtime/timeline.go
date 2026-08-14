package runtime

import (
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the TimelineService: the instance-line history
// of the workspace — every instance of one artifact line with its
// change log and the immutable object hash each instance points at.
// History emerges from the immutable payload archive (references +
// prev_hash lineage); the change log travels inside the payload.

// TimelineEntry is one instance of an artifact line.
type TimelineEntry struct {
	// Form is the canonical identity form of the instance.
	Form string
	// InstanceVersion is the identity's instance version.
	InstanceVersion int
	// Revision is the unit revision.
	Revision int
	// ObjectHash is the immutable payload the instance's reference
	// points at (SHA-256(unit.json || content)).
	ObjectHash string
	// ChangeLog is the instance's full transition history in
	// occurrence order (from the payload).
	ChangeLog []exchange.ChangeLogEntry
}

// TimelineService reads the instance-line history of the workspace.
// Concrete and documented — no interface type.
type TimelineService struct{ rt *Runtime }

// Line returns every instance of one artifact line — the identity
// (namespace, type token, id) across the whole workspace — sorted by
// instance-version (ascending: the line's history order). Each entry
// carries the decoded change log (from the payload) and the object
// hash (from the reference). An empty line returns an empty slice.
func (s *TimelineService) Line(ns, typeToken, id string) ([]TimelineEntry, error) {
	units, err := s.rt.Resolver.ResolveLine(ns, typeToken, id)
	if err != nil {
		return nil, err
	}
	out := make([]TimelineEntry, 0, len(units))
	for _, u := range units {
		out = append(out, TimelineEntry{
			Form:            u.CanonicalIdentityForm,
			InstanceVersion: u.Identity.InstanceVersion,
			Revision:        u.Revision,
			ObjectHash:      u.Digest,
			ChangeLog:       u.ChangeLog,
		})
	}
	return out, nil
}
