package exchange

import (
	"encoding/json"
	"fmt"
	"strings"
)

// This file implements the legacy RSF key-rename pass (spec-standard-v2
// §4): packages declaring Serialization Version "1.1" (or the pre-1.1
// line "1") carry the legacy kebab/snake JSON keys and are rewritten to
// the v2.0 camelCase keys before the strict decode — one decode path,
// two key spellings.
//
// The pass operates on the entry BYTES only: the digest verification
// (deserialize.go) recomputes over the package's own raw bytes, so the
// legacy digests stay authoritative. The rewritten bytes are never
// emitted — legacy packages are decoded, then re-emitted as v2.0 by the
// exporter.
//
// Key-order caveat (documented): the rewrite round-trips every entry
// through a map, which re-encodes object keys in lexicographic order.
// Field order is irrelevant for import (strict decode reads by key and
// the package digest covers the original bytes); determinism is an
// emitter contract, not an importer one.

// isLegacyHeader reports whether the package header declares a legacy
// serialization version ("1.1" or "1" — the versions whose entries carry
// the kebab/snake keys). The v2.0 header carries `serializationVersion`;
// the legacy header carries `serialization_version`. A header that is
// not valid JSON is reported as non-legacy: the strict decode reports it
// with the precise diagnostic.
func isLegacyHeader(headerData []byte) bool {
	var sniff struct {
		SerializationV1 string `json:"serialization_version"`
		SerializationV2 string `json:"serializationVersion"`
	}
	if err := json.Unmarshal(headerData, &sniff); err != nil {
		return false
	}
	v := sniff.SerializationV1
	if v == "" {
		v = sniff.SerializationV2
	}
	return v == LegacySerializationVersion || v == LegacySerializationVersionV1
}

// legacyKeyRename returns a reader whose JSON entries carry the v2.0
// camelCase keys. Content and attachment entries are opaque bytes and
// pass through untouched. A legacy entry that is not valid JSON is a
// deterministic *PackageError (a corrupt package, not a rename concern).
func legacyKeyRename(r *PackageReader) (*PackageReader, error) {
	out := &PackageReader{path: r.path}
	for _, name := range r.sortedEntries() {
		data, _ := r.Entry(name)
		var keyMap map[string]string
		switch {
		case name == "header.json":
			keyMap = legacyHeaderKeys
		case name == "manifest.json":
			keyMap = legacyManifestKeys
		case name == "declarations.json":
			keyMap = legacyDeclarationsKeys
		case name == "integrity.json":
			keyMap = legacyIntegrityKeys
		case strings.HasSuffix(name, "/unit.json"):
			keyMap = legacyUnitKeys
		default:
			out.add(name, data) // content payloads, attachments: opaque.
			continue
		}
		renamed, err := renameJSONKeys(data, keyMap)
		if err != nil {
			return nil, &PackageError{msg: fmt.Sprintf(
				"package %s entry %s is not valid legacy RSF JSON: %v", r.Path(), name, err)}
		}
		out.add(name, renamed)
	}
	return out, nil
}

// renameJSONKeys rewrites the object keys of one JSON value per keyMap,
// recursively (nested objects and array elements included). Keys absent
// from the map pass through verbatim. Values are never touched — the
// rename is key-only.
func renameJSONKeys(data []byte, keyMap map[string]string) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(renameJSONValue(v, keyMap))
}

// renameJSONValue is the recursive form of renameJSONKeys.
func renameJSONValue(v any, keyMap map[string]string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nk := keyMap[k]
			if nk == "" {
				nk = k
			}
			out[nk] = renameJSONValue(val, keyMap)
		}
		return out
	case []any:
		for i := range t {
			t[i] = renameJSONValue(t[i], keyMap)
		}
		return t
	default:
		return v
	}
}

// The per-block v1.1 -> v2.0 key maps (spec-standard-v2 §4 full mapping).
// Unknown keys pass through — the strict decode reports them with the
// reject-by-default diagnostic.

var legacyHeaderKeys = map[string]string{
	"serialization_version":   "serializationVersion",
	"exchange_format_version": "exchangeFormatVersion",
	"specification_version":   "specificationVersion",
	"package_identity_label":  "packageIdentityLabel",
	"export_scope":            "exportScope",
}

var legacyManifestKeys = map[string]string{
	"package_identity_label":  "packageIdentityLabel",
	"serialization_version":   "serializationVersion",
	"exchange_format_version": "exchangeFormatVersion",
	"specification_version":   "specificationVersion",
	"package_digest":          "packageDigest",
	"canonical_identity_form": "canonicalIdentityForm",
	"instance_version":        "instanceVersion",
	"content_representation":  "contentRepresentation",
	"content_file":            "contentFile",
	"unit_digest":             "unitDigest",
	"external_references":     "externalReferences",
}

var legacyUnitKeys = map[string]string{
	"canonical_identity_form": "canonicalIdentityForm",
	"state_vector":            "stateVector",
	"change_log":              "changeLog",
	"content-state":           "contentState",
	"execution-state":         "executionState",
	"planning-state":          "planningState",
	"container-state":         "containerState",
	"existence-state":         "existenceState",
	"note-state":              "noteState",
	"instance_version":        "instanceVersion",
	"dimensions_secondary":    "dimensionsSecondary",
}

var legacyDeclarationsKeys = map[string]string{
	"external_references": "externalReferences",
}

var legacyIntegrityKeys = map[string]string{
	"package_digest":          "packageDigest",
	"canonical_identity_form": "canonicalIdentityForm",
}
