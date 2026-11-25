package runtime

import (
	"time"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/machine"
)

// Freshness statuses (the closed set reported by machine.Freshness).
const (
	// FreshnessStale: the declared horizon is in the past as of `now`.
	FreshnessStale = "stale"
	// FreshnessCurrent: the declared horizon is today or later.
	FreshnessCurrent = "current"
	// FreshnessUndeclared: no horizon declared (or no clock injected).
	// The third state: not stale, not fresh, never a default.
	FreshnessUndeclared = "undeclared"
)

// FreshnessFor derives the freshness verdict of one unit as of `now`.
//
// The clock is always injected: this function never reads the wall
// clock, so the same inputs always produce the same verdict (the
// deterministic-conformance contract — freshness is a question asked
// with a clock, never a verdict baked into the corpus). A zero `now`
// yields the undeclared status without consulting the horizon: callers
// without a clock get the honest third state, not an accidental one.
//
// Day granularity is calendar-day granularity: both dates are
// normalised to midnight in `now`'s location, so "days until expiry" is
// a whole number of calendar days, negative once the horizon passed.
func FreshnessFor(u *exchange.Unit, now time.Time) machine.Freshness {
	f := machine.Freshness{Status: FreshnessUndeclared}
	if u == nil {
		return f
	}
	f.ValidUntil = u.ValidUntil
	f.LastConfirmed = u.LastConfirmed
	if now.IsZero() || u.ValidUntil == "" {
		return f
	}
	horizon, err := time.Parse("2006-01-02", u.ValidUntil)
	if err != nil {
		// Unreachable for conformant corpora (a malformed date is an R0
		// structural finding); a hand-built unit degrades to the honest
		// undeclared state instead of inventing a verdict.
		return f
	}
	days := int(calendarDay(horizon).Sub(calendarDay(now)) / (24 * time.Hour))
	f.DaysUntilExpiry = days
	if days < 0 {
		f.Status = FreshnessStale
	} else {
		f.Status = FreshnessCurrent
	}
	return f
}

// calendarDay truncates t to midnight in t's own location, so the day
// difference between two dates never depends on the time of day.
func calendarDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
