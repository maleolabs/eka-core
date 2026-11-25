package plugin

import (
	"reflect"
	"testing"
)

// The registry is the hardcoded source of truth for what "official"
// means; these tests pin the entries and the lookup API so a registry
// change is a deliberate, reviewed decision.

func TestOfficialRegistryLookup(t *testing.T) {
	repo, ok := OfficialRegistry.Lookup("mcp")
	if !ok {
		t.Fatal("mcp must be a registered official plugin")
	}
	if repo.Owner != "maleolabs" || repo.Name != "eka-mcp" {
		t.Errorf("mcp repo = %+v, want maleolabs/eka-mcp", repo)
	}
	if repo.String() != "maleolabs/eka-mcp" {
		t.Errorf("repo.String() = %q, want maleolabs/eka-mcp", repo.String())
	}
}

func TestOfficialRegistryLookupUnknown(t *testing.T) {
	if _, ok := OfficialRegistry.Lookup("nope"); ok {
		t.Error("an unregistered name must not resolve")
	}
	if _, ok := OfficialRegistry.Lookup(""); ok {
		t.Error("the empty name must not resolve")
	}
}

func TestOfficialRegistryIsOfficial(t *testing.T) {
	if !OfficialRegistry.IsOfficial("mcp") {
		t.Error("mcp must be official")
	}
	if OfficialRegistry.IsOfficial("nope") {
		t.Error("nope must not be official")
	}
}

func TestOfficialRegistryNames(t *testing.T) {
	names := OfficialRegistry.Names()
	if !reflect.DeepEqual(names, []string{"mcp"}) {
		t.Errorf("Names() = %v, want [mcp] (sorted)", names)
	}
}
