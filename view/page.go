package view

// This file implements the page window of the projection engine: the
// pure offset/limit window used by the CLI renderers for the
// --offset/--limit/--page retrieval flags. The projections never page
// themselves (except ContainersProjection.Page, which windows its
// slice and keeps its full totals); the renderers compute the window
// over each rendered list.

// Page is one offset/limit window over a list. Limit is always > 0
// (NewPage clamps), so Window and Pages never divide by zero.
type Page struct {
	Offset int
	Limit  int // > 0
}

// NewPage builds the window; a non-positive limit clamps to 1
// (defensive — the CLI validates the flag values).
func NewPage(offset, limit int) Page {
	if limit < 1 {
		limit = 1
	}
	return Page{Offset: offset, Limit: limit}
}

// Window returns the half-open [start, end) slice bounds of the window
// over a list of total items, clamped to the list: an offset beyond
// the end yields an empty window.
func (p Page) Window(total int) (start, end int) {
	start = p.Offset
	if start < 0 {
		start = 0
	}
	if start > total {
		start = total
	}
	end = start + p.Limit
	if end > total {
		end = total
	}
	return start, end
}

// Pages returns the page count of the window over a list of total
// items: ceil(total/limit); 0 when total == 0.
func (p Page) Pages(total int) int {
	if total == 0 {
		return 0
	}
	return (total + p.Limit - 1) / p.Limit
}
