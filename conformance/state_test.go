package conformance

import "testing"

func TestTypeTokenCount(t *testing.T) {
	if got := len(typeTokens); got != 27 {
		t.Fatalf("type token table has %d entries, want 27 (26 + cmt, ADR-019 D3)", got)
	}
}

func TestOwnedSets(t *testing.T) {
	// Expected owned sets per validation.md Rule 4 (type -> owned domains).
	want := map[string][]string{
		"vis":  {DomainContentState, DomainExistenceState},
		"str":  {DomainContentState, DomainExistenceState},
		"req":  {DomainContentState, DomainExistenceState},
		"scp":  {DomainContentState, DomainExistenceState},
		"epc":  {DomainContentState, DomainExistenceState},
		"plan": {DomainContentState, DomainPlanningState, DomainExistenceState},
		"ctr":  {DomainContainerState, DomainExistenceState},
		"tkt":  {},
		"sto":  {DomainExecutionState, DomainExistenceState},
		"ts":   {DomainExecutionState, DomainExistenceState},
		"bug":  {DomainExecutionState, DomainExistenceState},
		"td":   {DomainExecutionState, DomainExistenceState},
		"ch":   {DomainExecutionState, DomainExistenceState},
		"spk":  {DomainExecutionState, DomainExistenceState},
		"ses":  {DomainExistenceState},
		"rvw":  {DomainContentState, DomainExistenceState},
		"cmt":  {DomainContentState, DomainExistenceState, DomainNoteState},
		"adr":  {DomainContentState, DomainExistenceState},
		"dec":  {DomainContentState, DomainExistenceState},
		"arc":  {DomainContentState, DomainExistenceState},
		"spec": {DomainContentState, DomainExistenceState},
		"std":  {DomainContentState, DomainExistenceState},
		"run":  {DomainContentState, DomainExistenceState},
		"rel":  {DomainContentState, DomainExistenceState},
		"gls":  {DomainContentState, DomainExistenceState},
		"trc":  {DomainContentState, DomainExistenceState},
		"fnd":  {DomainContentState, DomainExistenceState},
	}
	for tok, expected := range want {
		info, ok := typeTokens[tok]
		if !ok {
			t.Errorf("token %q missing from type table", tok)
			continue
		}
		if !sameStrings(info.Owned, expected) {
			t.Errorf("type %q owned set = %v, want %v", tok, info.Owned, expected)
		}
	}
}

func TestContentStateVariant(t *testing.T) {
	cases := []struct {
		typ  string
		want []string
	}{
		{"adr", []string{"proposed", "accepted", "superseded"}},
		{"dec", []string{"draft", "accepted", "superseded"}},
		{"vis", []string{"draft", "review", "approved", "amended"}},
		{"plan", []string{"draft", "review", "approved", "amended"}},
		{"sto", []string{"draft", "review", "approved", "amended"}},
	}
	for _, c := range cases {
		if got := contentStateVariant(c.typ); !sameStrings(got, c.want) {
			t.Errorf("contentStateVariant(%q) = %v, want %v", c.typ, got, c.want)
		}
	}
}

func TestDimensionTokens(t *testing.T) {
	want := []string{
		"intent", "requirements", "architecture", "decisions", "specifications",
		"standards", "operations", "quality", "planning", "records", "research",
		"vocabulary",
	}
	if len(dimensionTokens) != len(want) {
		t.Fatalf("dimension table has %d entries, want %d", len(dimensionTokens), len(want))
	}
	for _, d := range want {
		if !dimensionTokens[d] {
			t.Errorf("dimension %q missing", d)
		}
	}
}

func TestIsLegalTransition(t *testing.T) {
	cases := []struct {
		name   string
		domain string
		typ    string
		from   string
		to     string
		legal  bool
	}{
		// Execution State: the explicit D1 table (ADR-019 §3) — forward
		// adjacent, one-step pull-back, cancel, re-activation.
		{"exec adjacent forward", DomainExecutionState, "sto", "planned", "todo", true},
		{"exec full chain", DomainExecutionState, "sto", "in-review", "done", true},
		{"exec skip", DomainExecutionState, "sto", "planned", "in-progress", false},
		{"exec pull-back one step", DomainExecutionState, "sto", "in-progress", "todo", true},
		{"exec pull-back one step review", DomainExecutionState, "sto", "in-review", "in-progress", true},
		{"exec multi-step revert", DomainExecutionState, "sto", "done", "in-progress", false},
		{"exec revert to planned", DomainExecutionState, "sto", "todo", "planned", false},
		{"exec noop", DomainExecutionState, "sto", "todo", "todo", false},
		{"exec from done", DomainExecutionState, "sto", "done", "done", false},
		{"exec cancel", DomainExecutionState, "sto", "in-progress", "canceled", true},
		{"exec cancel from done", DomainExecutionState, "sto", "done", "canceled", true},
		{"exec cancel from planned", DomainExecutionState, "sto", "planned", "canceled", true},
		{"exec re-activate", DomainExecutionState, "sto", "canceled", "todo", true},
		{"exec canceled to planned", DomainExecutionState, "sto", "canceled", "planned", false},
		{"exec canceled noop", DomainExecutionState, "sto", "canceled", "canceled", false},
		{"initial marker", DomainExecutionState, "sto", "-", "planned", true},
		// Other domains: forward-only, adjacency not required.
		{"living skip allowed", DomainContentState, "vis", "draft", "amended", true},
		{"living revert", DomainContentState, "vis", "approved", "draft", false},
		{"living adjacent", DomainContentState, "vis", "review", "approved", true},
		{"adr variant forward", DomainContentState, "adr", "proposed", "accepted", true},
		{"adr variant supersede", DomainContentState, "adr", "accepted", "superseded", true},
		{"adr variant revert", DomainContentState, "adr", "accepted", "proposed", false},
		{"decision variant", DomainContentState, "dec", "draft", "superseded", true},
		{"planning forward", DomainPlanningState, "plan", "draft", "immutable", true},
		{"planning revert", DomainPlanningState, "plan", "approved", "draft", false},
		{"container forward", DomainContainerState, "ctr", "planned", "active", true},
		{"container revert", DomainContainerState, "ctr", "active", "planned", false},
		{"container skip", DomainContainerState, "ctr", "planned", "completed", true},
		{"container forward active", DomainContainerState, "ctr", "active", "completed", true},
		{"container revert completed", DomainContainerState, "ctr", "completed", "active", false},
		{"container initial active", DomainContainerState, "ctr", "-", "active", true},
		{"container initial planned", DomainContainerState, "ctr", "-", "planned", true},
		{"existence skip", DomainExistenceState, "adr", "active", "retired", true},
		{"existence revert", DomainExistenceState, "adr", "archived", "active", false},
		// Phase: no transition ordering is defined.
		{"phase any move", DomainPhase, "plan", "discovery", "release", true},
		{"phase revert", DomainPhase, "plan", "maturity", "mvp", true},
	}
	for _, c := range cases {
		if got := isLegalTransition(c.domain, c.typ, c.from, c.to); got != c.legal {
			t.Errorf("%s: isLegalTransition(%s, %s, %q, %q) = %v, want %v",
				c.name, c.domain, c.typ, c.from, c.to, got, c.legal)
		}
	}
}

func TestNoteStateTransitions(t *testing.T) {
	// note-state (ADR-019 D4): forward-only, terminal after
	// resolved/dismissed.
	cases := []struct {
		from, to string
		legal    bool
	}{
		{"open", "resolved", true},
		{"open", "dismissed", true},
		{"resolved", "open", false},
		{"resolved", "dismissed", false},
		{"dismissed", "open", false},
		{"open", "open", false},
	}
	for _, c := range cases {
		if got := isLegalTransition(DomainNoteState, "cmt", c.from, c.to); got != c.legal {
			t.Errorf("note-state %q -> %q = %v, want %v", c.from, c.to, got, c.legal)
		}
	}
	if !sameStrings(DomainValues(DomainNoteState, "cmt"), []string{"open", "resolved", "dismissed"}) {
		t.Errorf("note-state values = %v, want the open/resolved/dismissed set", DomainValues(DomainNoteState, "cmt"))
	}
}

func TestDiscussesRelationshipField(t *testing.T) {
	fields := RelationshipFieldNames()
	if len(fields) != 7 || fields[5] != "discusses" || fields[6] != "replies-to" {
		t.Errorf("RelationshipFieldNames = %v, want seven fields ending in discusses, replies-to (ADR-019 D5/D8)", fields)
	}
	if !IsWorkItemType("sto") || IsWorkItemType("cmt") || len(WorkItemTypes()) != 6 {
		t.Errorf("work-item type set = %v, want the six gated types", WorkItemTypes())
	}
}

func TestPhaseValueSet(t *testing.T) {
	want := []string{"discovery", "mvp", "milestone", "release", "growth", "maturity", "sunset"}
	if !sameStrings(phaseValues, want) {
		t.Errorf("phaseValues = %v, want %v", phaseValues, want)
	}
}

func sameStrings(a, b []string) bool {
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
