package machine

import (
	"sort"

	"github.com/maleolabs/eka-core/exchange"
)

// Collection is the machine projection of a domain query: every unit of
// one Engineering Domain of the project, sorted by canonical form.
type Collection struct {
	Schema     string      `json:"schema"`
	Collection string      `json:"collection"` // "domain"
	Domain     string      `json:"domain"`
	Count      int         `json:"count"`
	Units      []*Document `json:"units"` // sorted by canonical form
	// Pagination is nil unless a page window was applied (Page).
	Pagination *Pagination `json:"pagination,omitempty"`
}

// NewCollection maps the units of one domain query to a machine
// Collection. The units are sorted by canonical form regardless of the
// input order (determinism contract: sorted inputs by canonical form);
// an empty result is an empty unit list, never null. Domain carries the
// canonical Engineering Domain name (e.g. "Execution").
func NewCollection(domain string, units []*exchange.Unit) (*Collection, error) {
	docs := make([]*Document, 0, len(units))
	for _, u := range units {
		d, err := NewDocument(u)
		if err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	// Canonical-form order is the collection contract (deterministic;
	// lexicographic on the canonical identity form — instance versions
	// are compared textually, matching the RSF canonical key ordering).
	sort.Slice(docs, func(i, j int) bool {
		return docs[i].CanonicalForm < docs[j].CanonicalForm
	})
	return &Collection{
		Schema:     Schema,
		Collection: "domain",
		Domain:     domain,
		Count:      len(docs),
		Units:      docs,
	}, nil
}

// Page windows the Units slice to [offset, offset+limit), keeping
// Count as the TOTAL unit count and setting the Pagination metadata
// (nil before the first window — the default output stays
// byte-identical to the unpaged schema). A limit of 0 (--offset given
// without --limit) windows to the end of the list.
func (c *Collection) Page(offset, limit int) {
	total := len(c.Units)
	start := offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end := total
	if limit > 0 && start+limit < end {
		end = start + limit
	}
	c.Units = c.Units[start:end]
	c.Pagination = paginationOf(offset, limit, total)
}

// Marshal serializes the Collection deterministically: the same
// formatting as Document.Marshal (two-space indent, trailing newline).
func (c *Collection) Marshal() ([]byte, error) {
	return marshal(c, false)
}

// MarshalCompact serializes the Collection as a single JSON line plus
// a single trailing newline — the compact form of the same
// deterministic collection (same field order, same values).
func (c *Collection) MarshalCompact() ([]byte, error) {
	return marshal(c, true)
}
