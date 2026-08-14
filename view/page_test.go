package view

import "testing"

// TestPageWindow: Window clamps the half-open [start, end) bounds to
// the list; an offset at or beyond the end yields an empty window.
func TestPageWindow(t *testing.T) {
	cases := []struct {
		name      string
		page      Page
		total     int
		wantStart int
		wantEnd   int
	}{
		{"full list", NewPage(0, 10), 3, 0, 3},
		{"middle window", NewPage(1, 2), 5, 1, 3},
		{"window past the end clamps", NewPage(3, 5), 5, 3, 5},
		{"empty list", NewPage(0, 5), 0, 0, 0},
		{"offset at the end is empty", NewPage(3, 2), 3, 3, 3},
		{"offset beyond the end is empty", NewPage(10, 2), 3, 3, 3},
		{"negative offset clamps to zero", NewPage(-2, 2), 5, 0, 2},
	}
	for _, c := range cases {
		start, end := c.page.Window(c.total)
		if start != c.wantStart || end != c.wantEnd {
			t.Errorf("%s: Window(%d) = (%d, %d), want (%d, %d)",
				c.name, c.total, start, end, c.wantStart, c.wantEnd)
		}
	}
}

// TestPagePages: Pages is ceil(total/limit) and 0 when total == 0.
func TestPagePages(t *testing.T) {
	cases := []struct {
		name  string
		page  Page
		total int
		want  int
	}{
		{"exact division", NewPage(0, 2), 4, 2},
		{"rounded up", NewPage(0, 2), 5, 3},
		{"single page", NewPage(0, 10), 5, 1},
		{"empty list", NewPage(0, 2), 0, 0},
		{"one item", NewPage(0, 3), 1, 1},
	}
	for _, c := range cases {
		if got := c.page.Pages(c.total); got != c.want {
			t.Errorf("%s: Pages(%d) = %d, want %d", c.name, c.total, got, c.want)
		}
	}
}

// TestNewPageClampsLimit: NewPage clamps a non-positive limit to 1
// (the Window/Pages math never divides by zero).
func TestNewPageClampsLimit(t *testing.T) {
	if got := NewPage(1, 0); got.Limit != 1 {
		t.Errorf("NewPage(1, 0).Limit = %d, want 1", got.Limit)
	}
	if got := NewPage(1, -3); got.Limit != 1 {
		t.Errorf("NewPage(1, -3).Limit = %d, want 1", got.Limit)
	}
	if got := NewPage(1, 4); got.Limit != 4 {
		t.Errorf("NewPage(1, 4).Limit = %d, want 4", got.Limit)
	}
}
