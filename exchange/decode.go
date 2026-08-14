package exchange

import "bytes"

// This file implements the standalone unit decoding helpers of the
// exchange model:
//
//	u, err := exchange.DecodeUnit(unitJSON, content)
//
// DecodeUnit is the read-side counterpart of MarshalUnit (emit.go): it
// reconstructs one exchange.Unit from its canonical unit.json bytes and
// its content payload bytes, applying the same reject-by-default
// unknown-field policy as the package loader (RSF §9.5). It exists for
// consumers that hold units as raw bytes without a surrounding package
// — the canonical store of the EKA Knowledge Runtime stores exactly
// (unit.json || content) per immutable payload and reconstructs the
// model on push; the integrity engine decodes every stored payload to
// verify it.
//
// Legacy tolerance (spec-standard-v2 §4): the canonical store may hold
// unit.json bytes written by a v1.1 snapshot (the store is schema-v3
// opaque storage — no migration, existing payloads stay readable). A
// unit.json carrying the legacy kebab/snake keys is normalized by the
// same key-rename pass the package loader uses before the strict
// decode, so legacy payloads reconstruct and re-emit as v2.0.

// DecodeUnit strictly decodes unitJSON into a Unit (unknown fields
// rejected, RSF §9.5) and attaches content as the unit's
// ContentPayload. The decoded unit carries everything the serializer
// emits: Identity, CanonicalIdentityForm, Revision, metadata,
// StateVector, ChangeLog, Relationships, Classification, Phase and the
// Content reference. It is the inverse of MarshalUnit for bytes
// produced by this package (and tolerates a trailing LF, the package
// entry normalization of RSF §9.3).
func DecodeUnit(unitJSON, content []byte) (*Unit, error) {
	var u Unit
	if err := strictDecode("unit.json", unitJSON, &u); err != nil {
		// Legacy key spelling: a v1.1/v1 unit.json (kebab/snake keys).
		// The sniff is key-presence on the raw bytes — a v2.0 unit.json
		// never contains the legacy spellings (camelCase everywhere).
		if isLegacyUnitJSON(unitJSON) {
			renamed, rerr := renameJSONKeys(unitJSON, legacyUnitKeys)
			if rerr == nil {
				err = strictDecode("unit.json", renamed, &u)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	u.ContentPayload = content
	return &u, nil
}

// isLegacyUnitJSON reports whether the unit.json bytes carry the legacy
// kebab/snake key spelling (state_vector, canonical_identity_form, ...).
// Presence sniff on the raw bytes: deterministic, no parsing — a v2.0
// unit.json contains only camelCase keys, so the legacy spellings cannot
// occur in it (values never carry these exact quoted strings).
func isLegacyUnitJSON(unitJSON []byte) bool {
	for _, marker := range [][]byte{
		[]byte(`"state_vector"`),
		[]byte(`"canonical_identity_form"`),
		[]byte(`"change_log"`),
		[]byte(`"content-state"`),
	} {
		if bytes.Contains(unitJSON, marker) {
			return true
		}
	}
	return false
}
