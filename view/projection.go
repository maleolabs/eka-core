package view

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// This file implements the projection registry: the closed set of
// named projections the engine can build. Each projection is an
// independent builder function; a future projection is added by
// registering a builder here — there is no pipeline to redesign.
//
// The registry is domain-first: the canonical projections are the five
// Engineering Domains (discovery, architecture, planning, execution,
// operations), the ticket projection, the board projection (all work
// items across every container), and the containers projection (every
// execution container line with its plan, items, tickets and
// lifecycle). The former sprint and wave projections are registered as
// aliases of execution: they resolve to the same builder, so `eka view
// sprint` renders byte-identical output to `eka view execution`.

// Projection is one view over the Knowledge Graph. The model types are
// plain data with deterministic ordering; rendering is the caller's
// concern.
type Projection interface {
	// Name returns the projection's registry name ("execution",
	// "ticket", "planning", ...). Aliases resolve to the canonical
	// projection, so the name is always canonical.
	Name() string
}

// ErrUnknownProjection is returned by Build for a projection name that
// is not registered (neither canonical nor alias).
var ErrUnknownProjection = errors.New("unknown projection")

// TargetNotFoundError reports an unresolvable ticket/document target
// with the available targets, so the CLI can fail with a helpful
// message.
type TargetNotFoundError struct {
	// Projection is the projection name the target belongs to.
	Projection string
	// Target is the unresolved target as given.
	Target string
	// Available lists the resolvable targets, sorted by canonical
	// identity (the ticket projection).
	Available []string
	// Hint replaces the available-list tail when set (the document
	// projection: the resolution grammar or the sync hint).
	Hint string
}

// Error renders the deterministic diagnostic.
func (e *TargetNotFoundError) Error() string {
	if e.Hint != "" {
		return fmt.Sprintf("%s projection: target %q not found (%s)",
			e.Projection, e.Target, e.Hint)
	}
	if len(e.Available) == 0 {
		return fmt.Sprintf("%s projection: target %q not found (the repository contains no tickets; run 'eka sync' to seed its knowledge)",
			e.Projection, e.Target)
	}
	return fmt.Sprintf("%s projection: target %q not found — available tickets: %s",
		e.Projection, e.Target, strings.Join(e.Available, ", "))
}

// builder constructs one projection over the graph. target is the
// optional user-supplied target; projections that do not use it ignore
// it.
type builder func(g *Graph, target string) (Projection, error)

// registry maps the canonical projection names to their independent
// builders.
var registry = map[string]builder{
	"discovery":    buildDiscovery,
	"architecture": buildArchitecture,
	"planning":     buildPlanning,
	"execution":    buildExecution,
	"operations":   buildOperations,
	"ticket":       buildTicket,
	"board":        buildBoard,
	"document":     buildDocument,
	"containers":   buildContainers,
}

// aliases maps alias names to their canonical projection. An alias is
// a registered projection name (IsProjection reports it) that resolves
// to the canonical builder, producing identical output.
var aliases = map[string]string{
	"sprint": "execution",
	"wave":   "execution",
}

// Projections returns the canonical projection names, sorted —
// deterministic regardless of map iteration order. Aliases are
// excluded; see Aliases.
func Projections() []string {
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Aliases returns the registered alias names, sorted.
func Aliases() []string {
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AliasTarget returns the canonical projection an alias resolves to,
// or "" when name is not a registered alias.
func AliasTarget(name string) string {
	return aliases[name]
}

// HelpList renders the canonical projections and their aliases for
// diagnostics: "architecture, discovery, execution, operations,
// planning, ticket (aliases: sprint, wave)". Deterministic.
func HelpList() string {
	return fmt.Sprintf("%s (aliases: %s)",
		strings.Join(Projections(), ", "), strings.Join(Aliases(), ", "))
}

// IsProjection reports whether name is a registered projection —
// canonical or alias.
func IsProjection(name string) bool {
	if _, ok := registry[name]; ok {
		return true
	}
	_, ok := aliases[name]
	return ok
}

// Build constructs the named projection over the graph. An alias name
// resolves to its canonical projection, so the returned model and its
// Name are always canonical. It returns ErrUnknownProjection (wrapped,
// with the canonical projections and aliases listed) for unregistered
// names and *TargetNotFoundError when the ticket target does not
// resolve. A missing target on a target-requiring projection is an
// error.
func Build(name string, g *Graph, target string) (Projection, error) {
	b, ok := registry[name]
	if !ok {
		if canonical, isAlias := aliases[name]; isAlias {
			b = registry[canonical]
		} else {
			return nil, fmt.Errorf("%w: %q — available projections: %s",
				ErrUnknownProjection, name, HelpList())
		}
	}
	return b(g, target)
}
