package conformance

import (
	"sort"
	"testing"
)

// TestDomainForTokenComplete verifies the canonical mapping table is
// complete and duplicate-free: every one of the 28 type tokens maps to a
// home domain, no token maps twice (map keys are unique by construction),
// and the token set of the domain table equals the token set of the
// type/state taxonomy exactly (no drift between the two tables).
func TestDomainForTokenComplete(t *testing.T) {
	if len(tokenDomain) != 28 {
		t.Fatalf("token domain table has %d entries, want 28 (26 + cmt, ADR-019, + mbr, ADR-029)", len(tokenDomain))
	}
	for tok := range typeTokens {
		d, ok := DomainForToken(tok)
		if !ok {
			t.Errorf("token %q has no home domain", tok)
			continue
		}
		if !IsDomain(string(d)) {
			t.Errorf("token %q maps to non-canonical domain %q", tok, d)
		}
	}
	if len(tokenDomain) != len(typeTokens) {
		t.Fatalf("token domain table (%d) and type token table (%d) disagree in size",
			len(tokenDomain), len(typeTokens))
	}
	for tok := range tokenDomain {
		if _, ok := typeTokens[tok]; !ok {
			t.Errorf("domain table maps unknown token %q", tok)
		}
	}
	// Every dimension's domain must exist too (no stray mappings).
	for dim := range dimensionDomain {
		if !dimensionTokens[dim] {
			t.Errorf("domain table maps unknown dimension %q", dim)
		}
	}
}

// TestDomainForTokenValues pins the exact mapping (the Wave 1 table).
func TestDomainForTokenValues(t *testing.T) {
	want := map[string]Domain{
		"vis": Discovery, "str": Discovery, "req": Discovery, "fnd": Discovery,
		"arc": Architecture, "adr": Architecture, "dec": Architecture,
		"spec": Architecture, "std": Architecture, "gls": Architecture,
		"scp": Planning, "epc": Planning, "plan": Planning, "trc": Planning,
		"rvw": Execution, "ctr": Execution, "tkt": Execution,
		"sto": Execution, "ts": Execution, "bug": Execution, "td": Execution,
		"ch": Execution, "spk": Execution, "ses": Execution,
		"cmt": Execution, "mbr": Execution,
		"run": Operations, "rel": Operations,
	}
	for tok, wantDomain := range want {
		got, ok := DomainForToken(tok)
		if !ok || got != wantDomain {
			t.Errorf("DomainForToken(%q) = %q, %v; want %q, true", tok, got, ok, wantDomain)
		}
	}
	if d, ok := DomainForToken("bogus"); ok || d != "" {
		t.Errorf("DomainForToken(bogus) = %q, %v; want empty, false", d, ok)
	}
}

// TestDomainForDimensionComplete verifies all 12 dimensions map and the
// table matches the dimension vocabulary exactly.
func TestDomainForDimensionComplete(t *testing.T) {
	if len(dimensionDomain) != 12 {
		t.Fatalf("dimension domain table has %d entries, want 12", len(dimensionDomain))
	}
	for dim := range dimensionTokens {
		if _, ok := DomainForDimension(dim); !ok {
			t.Errorf("dimension %q has no home domain", dim)
		}
	}
	if len(dimensionDomain) != len(dimensionTokens) {
		t.Fatalf("dimension domain table (%d) and dimension table (%d) disagree in size",
			len(dimensionDomain), len(dimensionTokens))
	}
}

// TestDomainForDimensionValues pins the exact dimension mapping.
func TestDomainForDimensionValues(t *testing.T) {
	want := map[string]Domain{
		"intent": Discovery, "requirements": Discovery, "research": Discovery,
		"architecture": Architecture, "decisions": Architecture,
		"specifications": Architecture, "standards": Architecture,
		"vocabulary": Architecture,
		"planning":   Planning,
		"quality":    Execution,
		"operations": Operations, "records": Operations,
	}
	for dim, wantDomain := range want {
		got, ok := DomainForDimension(dim)
		if !ok || got != wantDomain {
			t.Errorf("DomainForDimension(%q) = %q, %v; want %q, true", dim, got, ok, wantDomain)
		}
	}
	if d, ok := DomainForDimension("bogus"); ok || d != "" {
		t.Errorf("DomainForDimension(bogus) = %q, %v; want empty, false", d, ok)
	}
}

// TestStratumOrdering pins stratum numbers 1..5 and the StrataAbove
// set for every domain.
func TestStratumOrdering(t *testing.T) {
	wantStratum := map[Domain]int{
		Discovery: 1, Architecture: 2, Planning: 3, Execution: 4, Operations: 5,
	}
	for d, want := range wantStratum {
		if got := Stratum(d); got != want {
			t.Errorf("Stratum(%s) = %d, want %d", d, got, want)
		}
	}
	if got := Stratum("bogus"); got != 0 {
		t.Errorf("Stratum(bogus) = %d, want 0", got)
	}

	wantAbove := map[Domain][]Domain{
		Discovery:    {},
		Architecture: {Discovery},
		Planning:     {Discovery, Architecture},
		Execution:    {Discovery, Architecture, Planning},
		Operations:   {Discovery, Architecture, Planning, Execution},
	}
	for d, want := range wantAbove {
		if got := StrataAbove(d); !sameDomains(got, want) {
			t.Errorf("StrataAbove(%s) = %v, want %v", d, got, want)
		}
	}
}

// sameDomains reports slice equality (order matters).
func sameDomains(a, b []Domain) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDomainNamesSorted verifies the sorted, deduplicated name list.
func TestDomainNamesSorted(t *testing.T) {
	names := DomainNames()
	want := []string{"Architecture", "Discovery", "Execution", "Operations", "Planning"}
	if !sameStrings(names, want) {
		t.Errorf("DomainNames() = %v, want %v", names, want)
	}
	if !sort.StringsAreSorted(names) {
		t.Errorf("DomainNames() = %v, not sorted", names)
	}
	if len(names) != 5 {
		t.Errorf("DomainNames() has %d entries, want 5", len(names))
	}
}

// TestIsDomain covers valid and invalid spellings (case-sensitive).
func TestIsDomain(t *testing.T) {
	for _, d := range domains {
		if !IsDomain(string(d)) {
			t.Errorf("IsDomain(%q) = false, want true", d)
		}
	}
	for _, bad := range []string{"", "execution", "EXECUTION", "Execution ", "Domain", "bogus"} {
		if IsDomain(bad) {
			t.Errorf("IsDomain(%q) = true, want false", bad)
		}
	}
}
