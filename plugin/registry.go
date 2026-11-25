package plugin

import "sort"

// The official plugin registry: the hardcoded list of plugins the EKA
// ecosystem ships and trusts. It maps a stable plugin name (the
// "eka-<name>" executable identity, e.g. "mcp") to the canonical GitHub
// repository its release assets are fetched from.
//
// Two-tier trust model (sto:plugin-trust-model): an official registry
// entry is full-trust by definition — the registry IS the trust
// decision, made by the EKA maintainers. Registry-listed names install
// without a prompt; anything else is third-party and requires explicit
// consent after its source and capabilities are surfaced. The registry
// is the single source of truth for what "official" means: a name is
// official iff it resolves here (all entries are maleolabs-maintained
// today).

// Repo is a canonical GitHub repository reference (owner/name), e.g.
// {Owner: "maleolabs", Name: "eka-mcp"}.
type Repo struct {
	Owner string
	Name  string
}

// String renders the canonical "owner/name" form.
func (r Repo) String() string { return r.Owner + "/" + r.Name }

// Registry is the hardcoded registry of official EKA plugins: plugin
// name -> canonical source repository. It is the single source of
// truth for what "official" means.
type Registry struct {
	entries map[string]Repo
}

// OfficialRegistry is the built-in registry. It is a package-level
// value so the CLI resolves plugin names against one canonical list.
var OfficialRegistry = Registry{
	entries: map[string]Repo{
		// Official EKA plugins.
		"mcp": {Owner: "maleolabs", Name: "eka-mcp"},
	},
}

// Lookup resolves a plugin name to its official source repository.
func (r Registry) Lookup(name string) (Repo, bool) {
	repo, ok := r.entries[name]
	return repo, ok
}

// IsOfficial reports whether name is a registered official plugin.
func (r Registry) IsOfficial(name string) bool {
	_, ok := r.entries[name]
	return ok
}

// Names lists the known official plugin names in sorted order (used
// for the unknown-plugin refusal and help text).
func (r Registry) Names() []string {
	names := make([]string, 0, len(r.entries))
	for n := range r.entries {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
