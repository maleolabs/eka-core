// Package conformance_test (external test package) is used by this file on
// purpose: the anti-drift check spans two packages. conformance must not
// import exchange (the dependency runs the other way — exchange projects
// units onto conformance.Artifact), so the unit side of the envelope can only
// be inspected from outside the package under test.
package conformance_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/maleolabs/eka-core/conformance"
	"github.com/maleolabs/eka-core/exchange"
)

// envelope_manifest_test.go is the drift alarm for the ADR-035 capture
// envelope. The envelope is declared once, as data
// (conformance.EnvelopeManifest); everything else — the adapter validation,
// the JSON allowlist, the exchange unit projection, both renderers and the
// reverse projection — is derived from or checked against that declaration.
//
// The failure guarded here has already happened once: a field is added to
// conformance.Artifact and to the JSON allowlist but not to the
// exchange.Unit projection, so export writes nothing, import has nothing to
// read, and no finding ever mentions it. Months pass before anyone notices
// that a claim is missing from every package. The checks below make both
// halves of that mistake fail loudly and immediately:
//
//   - a table entry with no field on exchange.Unit (or no field on
//     conformance.Artifact, which the record closures also pin at compile
//     time);
//   - an Artifact envelope field that no table entry declares (so nothing
//     validates, projects or renders it).

// nonEnvelopeArtifactFields is the explicit baseline of conformance.Artifact
// fields that do NOT belong to the capture envelope. It exists so the second
// direction above has a closed set to check against: an Artifact field is
// either in this list or declared by the envelope table. Adding a field to
// Artifact therefore forces a decision — declare it, or baseline it here.
var nonEnvelopeArtifactFields = []string{
	"RelPath", "AbsPath",
	"Namespace", "Type", "ID", "InstanceVersion", "Revision",
	"Author", "AuthorKind", "Created", "Updated",
	"States", "Phase", "HasPhase", "HasPhaseKey",
	"Dimension", "HasDimension", "DimensionsSecondary", "Domain",
	"Relations", "ChangeLog", "BodyLines", "ContentFields",
}

// envelopeFieldName maps a camelCase metadata key to the Go field name both
// models use for it (provenance -> Provenance, sourceCommitSha ->
// SourceCommitSha). The mapping is the naming convention, not a guess:
// a rename that breaks it fails this test, which is the point.
func envelopeFieldName(key string) string {
	if key == "" {
		return ""
	}
	return strings.ToUpper(key[:1]) + key[1:]
}

// sameShape reports whether two types carry the same values. It is not plain
// assignability: conformance.CaptureMeta and exchange.CaptureMeta are
// separate declarations mirroring one contract (an import cycle forbids
// sharing the type), so structs are compared field by field, order included.
func sameShape(a, b reflect.Type) bool {
	if a == b {
		return true
	}
	if a.Kind() != b.Kind() || a.Kind() != reflect.Struct || a.NumField() != b.NumField() {
		return false
	}
	for i := 0; i < a.NumField(); i++ {
		fa, fb := a.Field(i), b.Field(i)
		if fa.Name != fb.Name || !sameShape(fa.Type, fb.Type) {
			return false
		}
	}
	return true
}

// TestEnvelopeManifestMatchesBothModels: every declared envelope field
// exists on the artifact model and on the exchange unit, with a shape the
// unit can be projected onto the artifact with — and a declared paired flag
// exists on both. A new table entry without a unit field fails here before
// any package can silently drop the value.
func TestEnvelopeManifestMatchesBothModels(t *testing.T) {
	manifest := conformance.EnvelopeManifest()
	if len(manifest) == 0 {
		t.Fatal("the envelope manifest is empty; the envelope table lost its declaration")
	}

	artifact := reflect.TypeOf(conformance.Artifact{})
	unit := reflect.TypeOf(exchange.Unit{})

	for _, entry := range manifest {
		name := envelopeFieldName(entry.Key)

		af, ok := artifact.FieldByName(name)
		if !ok {
			t.Errorf("envelope field %q: conformance.Artifact has no %s field", entry.Key, name)
			continue
		}
		uf, ok := unit.FieldByName(name)
		if !ok {
			t.Errorf("envelope field %q: exchange.Unit has no %s field; the table declares it but the unit projection cannot carry it", entry.Key, name)
			continue
		}
		if !sameShape(af.Type, uf.Type) {
			t.Errorf("envelope field %q: exchange.Unit.%s is %s but conformance.Artifact.%s is %s; the unit cannot be projected onto the artifact",
				entry.Key, name, uf.Type, name, af.Type)
		}

		if entry.Flag == "" {
			continue
		}
		if _, ok := unit.FieldByName(entry.Flag); !ok {
			t.Errorf("envelope field %q: exchange.Unit has no %s flag; a declared zero would be indistinguishable from absence", entry.Key, entry.Flag)
		}
		if _, ok := artifact.FieldByName(entry.Flag); !ok {
			t.Errorf("envelope field %q: conformance.Artifact has no %s flag; the declared zero would be lost before export", entry.Key, entry.Flag)
		}
	}
}

// TestArtifactEnvelopeFieldsAreDeclared is the other direction: every
// conformance.Artifact field is either baseline (not envelope) or declared by
// the envelope table. An envelope field added straight to the model — to be
// read by an adapter, written by a renderer — fails here because nothing in
// the table validates, allowlists or projects it.
func TestArtifactEnvelopeFieldsAreDeclared(t *testing.T) {
	claimed := map[string]bool{}
	for _, entry := range conformance.EnvelopeManifest() {
		claimed[envelopeFieldName(entry.Key)] = true
		if entry.Flag != "" {
			claimed[entry.Flag] = true
		}
	}
	baseline := map[string]bool{}
	for _, name := range nonEnvelopeArtifactFields {
		if claimed[name] {
			t.Errorf("baseline lists %s, but the envelope table declares it; the two lists must not overlap", name)
		}
		baseline[name] = true
	}

	artifact := reflect.TypeOf(conformance.Artifact{})
	for i := 0; i < artifact.NumField(); i++ {
		name := artifact.Field(i).Name
		if baseline[name] || claimed[name] {
			continue
		}
		t.Errorf("conformance.Artifact field %s is neither an envelope table entry nor in the non-envelope baseline: a field with no table entry is never validated, allowlisted, projected or rendered", name)
	}
}
