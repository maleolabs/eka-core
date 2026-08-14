package view

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// TestProjectionsListsRegisteredNames verifies the registry surface:
// canonical names sorted (aliases excluded), aliases listed separately,
// and IsProjection accepting both.
func TestProjectionsListsRegisteredNames(t *testing.T) {
	want := []string{"architecture", "board", "containers", "discovery", "document", "execution", "operations", "planning", "ticket"}
	if got := Projections(); !reflect.DeepEqual(got, want) {
		t.Errorf("Projections() = %v, want %v", got, want)
	}
	wantAliases := []string{"sprint", "wave"}
	if got := Aliases(); !reflect.DeepEqual(got, wantAliases) {
		t.Errorf("Aliases() = %v, want %v", got, wantAliases)
	}
	for _, name := range append(want, wantAliases...) {
		if !IsProjection(name) {
			t.Errorf("IsProjection(%q) = false", name)
		}
	}
	if IsProjection("bogus") {
		t.Error("IsProjection(bogus) must be false")
	}
}

// TestAliasTarget verifies the alias -> canonical mapping used by the
// CLI help.
func TestAliasTarget(t *testing.T) {
	for alias, want := range map[string]string{"sprint": "execution", "wave": "execution"} {
		if got := AliasTarget(alias); got != want {
			t.Errorf("AliasTarget(%q) = %q, want %q", alias, got, want)
		}
	}
	for _, name := range []string{"execution", "bogus"} {
		if got := AliasTarget(name); got != "" {
			t.Errorf("AliasTarget(%q) = %q, want \"\"", name, got)
		}
	}
}

// TestHelpList verifies the deterministic diagnostic listing canonical
// projections plus aliases.
func TestHelpList(t *testing.T) {
	want := "architecture, board, containers, discovery, document, execution, operations, planning, ticket (aliases: sprint, wave)"
	if got := HelpList(); got != want {
		t.Errorf("HelpList() = %q, want %q", got, want)
	}
}

// TestUnknownProjection verifies Build's error contract for unregistered
// names: ErrUnknownProjection, wrapped with the canonical projections
// and aliases listed.
func TestUnknownProjection(t *testing.T) {
	g := loadFixture(t, "valid")
	_, err := Build("bogus", g, "")
	if !errors.Is(err, ErrUnknownProjection) {
		t.Errorf("Build(bogus) error = %v, want ErrUnknownProjection", err)
	}
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("Build(bogus) error must name the projection, got %v", err)
	}
	for _, want := range []string{
		"architecture", "discovery", "execution", "operations", "planning", "ticket",
		"sprint", "wave",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Build(bogus) error must list %q, got %v", want, err)
		}
	}
}

// TestTargetNotFound verifies the helpful TargetNotFoundError: the
// unresolved target plus the sorted available ticket list.
func TestTargetNotFound(t *testing.T) {
	g := loadFixture(t, "valid")
	_, err := Build("ticket", g, "tkt-ghost")
	var tne *TargetNotFoundError
	if !errors.As(err, &tne) {
		t.Fatalf("Build(ticket, tkt-ghost) error = %v, want *TargetNotFoundError", err)
	}
	if tne.Target != "tkt-ghost" {
		t.Errorf("TargetNotFoundError.Target = %q", tne.Target)
	}
	msg := tne.Error()
	if !strings.Contains(msg, "tkt-ghost") || !strings.Contains(msg, "available tickets") {
		t.Errorf("error must be helpful, got %q", msg)
	}
	want := []string{"bug-delta", "ch-epsilon", "sto-alpha", "sto-alpha-dup", "sto-beta", "sto-beta-multi", "sto-legacy", "ts-gamma", "unresolved"}
	if !reflect.DeepEqual(tne.Available, want) {
		t.Errorf("Available = %v, want %v", tne.Available, want)
	}

	// No tickets at all: distinct, still helpful message.
	empty := NewGraph(".", nil)
	_, err = Build("ticket", empty, "tkt-ghost")
	if !errors.As(err, &tne) {
		t.Fatalf("Build on empty graph error = %v, want *TargetNotFoundError", err)
	}
	if !strings.Contains(tne.Error(), "contains no tickets") {
		t.Errorf("empty-graph error must say so, got %q", tne.Error())
	}
}

// TestTargetRequired verifies the ticket projection refuses a missing
// target.
func TestTargetRequired(t *testing.T) {
	g := loadFixture(t, "valid")
	if _, err := Build("ticket", g, ""); err == nil {
		t.Error("Build(ticket, \"\") must fail")
	} else if !strings.Contains(err.Error(), "requires a target") {
		t.Errorf("error must explain the requirement, got %q", err.Error())
	}
}

// TestAliasesIgnoreTarget verifies the execution aliases resolve to the
// canonical projection and ignore the optional target argument.
func TestAliasesIgnoreTarget(t *testing.T) {
	g := loadFixture(t, "valid")
	p, err := Build("sprint", g, "tkt-ghost")
	if err != nil {
		t.Fatalf("sprint with a target must not fail: %v", err)
	}
	if p.Name() != "execution" {
		t.Errorf("Build(sprint).Name() = %q, want execution (canonical)", p.Name())
	}
	if _, err := Build("wave", g, "anything"); err != nil {
		t.Errorf("wave with a target must not fail: %v", err)
	}
}
