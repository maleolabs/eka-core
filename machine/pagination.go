package machine

// This file implements the pagination metadata of the machine
// interface: the window bookkeeping appended to a collection when a
// retrieval asks for a page window (--offset/--limit/--page).

// Pagination carries the effective window of a paged collection
// retrieval. It is nil (absent from the JSON — omitempty) unless the
// retrieval applied a window, so the default output stays
// byte-identical to the unpaged schema.
type Pagination struct {
	// Offset is the 0-based effective offset of the window.
	Offset int `json:"offset"`
	// Limit is the window size (0 when the window runs to the end of
	// the collection).
	Limit int `json:"limit"`
	// Page is the 1-based effective page (offset/limit+1 when limit >
	// 0, else 1).
	Page int `json:"page"`
	// Total is the total item count the window was applied to.
	Total int `json:"total"`
	// Pages is the total page count (ceil(total/limit); 0 when
	// total == 0).
	Pages int `json:"pages"`
}
