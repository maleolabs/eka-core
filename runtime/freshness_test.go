package runtime

import (
	"testing"
	"time"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// TestFreshnessFor pins the freshness verdict table: the three states
// (undeclared/current/stale), the injected-clock contract (no wall
// clock inside), and the degradation of a malformed horizon. The clock
// is always explicit, so the table is fully deterministic.
func TestFreshnessFor(t *testing.T) {
	day := func(s string) time.Time {
		d, err := time.Parse("2006-01-02", s)
		if err != nil {
			t.Fatalf("bad test date %q: %v", s, err)
		}
		return d
	}
	cases := []struct {
		name      string
		unit      *exchange.Unit
		now       time.Time
		wantState string
		wantDays  int
	}{
		{
			name:      "no horizon is undeclared even with a clock",
			unit:      &exchange.Unit{LastConfirmed: "2026-01-01"},
			now:       day("2026-11-18"),
			wantState: FreshnessUndeclared,
			wantDays:  0,
		},
		{
			name:      "zero clock is undeclared even with a horizon",
			unit:      &exchange.Unit{ValidUntil: "2027-01-01"},
			wantState: FreshnessUndeclared,
			wantDays:  0,
		},
		{
			name:      "horizon in the future is current",
			unit:      &exchange.Unit{ValidUntil: "2027-01-01", LastConfirmed: "2026-01-01"},
			now:       day("2026-11-18"),
			wantState: FreshnessCurrent,
			wantDays:  44,
		},
		{
			name:      "horizon today is current (day zero)",
			unit:      &exchange.Unit{ValidUntil: "2026-11-18"},
			now:       day("2026-11-18"),
			wantState: FreshnessCurrent,
			wantDays:  0,
		},
		{
			name:      "horizon in the past is stale with negative days",
			unit:      &exchange.Unit{ValidUntil: "2026-01-01"},
			now:       day("2026-11-18"),
			wantState: FreshnessStale,
			wantDays:  -321,
		},
		{
			name:      "malformed horizon degrades to undeclared, never stale",
			unit:      &exchange.Unit{ValidUntil: "2026-13-45"},
			now:       day("2026-11-18"),
			wantState: FreshnessUndeclared,
			wantDays:  0,
		},
		{
			name:      "time of day does not move the day count",
			unit:      &exchange.Unit{ValidUntil: "2027-01-01"},
			now:       day("2026-11-18").Add(23*time.Hour + 59*time.Minute),
			wantState: FreshnessCurrent,
			wantDays:  44,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FreshnessFor(c.unit, c.now)
			if got.Status != c.wantState {
				t.Errorf("status = %q, want %q", got.Status, c.wantState)
			}
			if got.DaysUntilExpiry != c.wantDays {
				t.Errorf("daysUntilExpiry = %d, want %d", got.DaysUntilExpiry, c.wantDays)
			}
			if c.unit == nil {
				return
			}
			if got.ValidUntil != c.unit.ValidUntil || got.LastConfirmed != c.unit.LastConfirmed {
				t.Errorf("verdict (%q, %q) does not echo the unit's (%q, %q)",
					got.ValidUntil, got.LastConfirmed, c.unit.ValidUntil, c.unit.LastConfirmed)
			}
		})
	}
}

// TestFreshnessForNilUnit keeps the nil guard honest: a nil unit is the
// undeclared state, never a panic.
func TestFreshnessForNilUnit(t *testing.T) {
	got := FreshnessFor(nil, time.Now())
	if got.Status != FreshnessUndeclared {
		t.Fatalf("status = %q, want %q", got.Status, FreshnessUndeclared)
	}
}

// TestUnitFromDraftCarriesFreshness proves the draft-publish path keeps
// the declared horizon: the artifact fields survive into the unit (the
// read-side counterpart of the artifact -> unit -> artifact round trip).
func TestUnitFromDraftCarriesFreshness(t *testing.T) {
	a := &conformance.Artifact{
		Namespace: "eka", Type: "adr", ID: "x", InstanceVersion: 1, Revision: 1,
		ValidUntil: "2027-01-01", LastConfirmed: "2026-11-18",
		States: map[string]string{conformance.DomainContentState: "proposed"},
	}
	u := unitFromDraft(a, 1)
	if u.ValidUntil != "2027-01-01" || u.LastConfirmed != "2026-11-18" {
		t.Fatalf("unitFromDraft lost the horizon: validUntil=%q lastConfirmed=%q", u.ValidUntil, u.LastConfirmed)
	}
}
