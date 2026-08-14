package exchange

// This file implements the package identity helpers (RSF §4.1): the
// namespace used in the Package Identity Label and the label itself. Both
// are pure functions of the built model (builder.go); there is no
// standalone repository discovery API — the export pipeline reads the
// repository once via load.go (conformance.Scan) and derives everything
// from the loaded working set.

// labelNamespace returns the namespace used in the Package Identity Label
// (RSF §4.1): the single namespace if all selected units share one,
// otherwise the lexicographically smallest. An empty package (no selected
// units) has no namespace: "" is returned and the label omits the
// namespace component (documented v1 decision — an empty Repository-scope
// package is still a valid, deterministic package).
func labelNamespace(namespaces []string) (string, error) {
	if len(namespaces) == 0 {
		return "", nil
	}
	ns := namespaces[0]
	for _, n := range namespaces[1:] {
		if n < ns {
			ns = n
		}
	}
	return ns, nil
}

// PackageIdentityLabel builds the deterministic package label (RSF §4.1):
// "rsf-<scope>-<namespace>-<serialization-version>". When the package has
// no namespace (empty Repository-scope export) the namespace component is
// omitted: "rsf-<scope>-<serialization-version>".
func PackageIdentityLabel(scope ScopeKind, namespace string) string {
	return PackageIdentityLabelVersion(scope, namespace, SerializationVersion)
}

// PackageIdentityLabelVersion builds the package label for a given
// Serialization Version; used for the current version on export and for
// legacy-version packages at import (self-consistency, RSF §8.2).
func PackageIdentityLabelVersion(scope ScopeKind, namespace, serializationVersion string) string {
	if namespace == "" {
		return "rsf-" + string(scope) + "-" + serializationVersion
	}
	return "rsf-" + string(scope) + "-" + namespace + "-" + serializationVersion
}
