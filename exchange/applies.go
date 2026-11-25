package exchange

import (
	"encoding/json"

	"github.com/maleolabs/eka-core/pathglob"
)

// This file implements the read side of the EKA Standard 1.5 applicability
// declaration (specification/exchange.md, "Content: applicability
// declaration (optional)"): the extraction of a unit's optional `appliesTo`
// scope from its structured content payload. It is the ONE declaration
// reader, shared by the store-backed Context Engine (contexts) and the
// snapshot-backed projection, so the two derive the same scope from the same
// unit and neither carries a copy.

// AppliesTo reads a unit's optional `appliesTo` declaration from its content
// payload. declared=false when the payload is not a JSON object or carries no
// `appliesTo` key (absence is not a defect). One implementation, shared by the
// store-backed Context Engine and the snapshot-backed projection.
//
// A present but malformed declaration is an error — the declaration cannot be
// read, so it is reported, never silently ignored. pathglob.Interpret owns the
// definition of "malformed" (a value that is not an array, an element that is
// not a string, or a pattern that cannot be compiled).
func AppliesTo(u *Unit) (patterns []pathglob.Pattern, declared bool, err error) {
	var content map[string]any
	if json.Unmarshal(u.ContentPayload, &content) != nil || content == nil {
		return nil, false, nil
	}
	raw, ok := content["appliesTo"]
	if !ok {
		return nil, false, nil
	}
	patterns, err = pathglob.Interpret(raw)
	if err != nil {
		return nil, false, err
	}
	return patterns, true, nil
}
