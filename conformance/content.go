package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// This file implements the content compilation and the canonical content
// encoding of the v2.0 pipeline (spec-standard-v2 §3.3, §6):
//
//	CompileContentObject  authoring -> the canonical content object
//	ContentJSON           the canonical compact payload encoding
//
// The compiler owns the canonicalization: it parses any authoring
// encoding (pretty JSON file or Markdown) and re-emits the content
// object canonically, so identical knowledge always yields identical
// payload bytes. The helpers live in the authoring-adapter layer
// (conformance), the common bottom of compile/, exchange/ and runtime/ —
// the repository path (exchange/load.go) and the draft path
// (runtime/draft.go) invoke them through their own build steps.

// CompileContentObject compiles an artifact's content into the canonical
// content object (map[string]any):
//
//   - JSON-native artifacts: the structured content object verbatim
//     (already the §3.2 shape).
//   - Markdown artifacts: the body compiled per spec-standard-v2 §6 —
//     split on "## " heading boundaries, each heading mapped to its
//     camelCase key via SectionKey (required sections by name match,
//     unknown headings become extra keys), section text trimmed; content
//     before the first heading (the preamble) is dropped (the §6
//     decision: the v1.1 gate already required the sections, so a
//     conformant document always has them).
//
// Documented interpretation of the §6 preamble decision: the projection
// header line (rule 8's marker, validation.md) is NOT knowledge content
// — it is the structural projection marker a conformant tkt-/ctr-
// artifact must carry. When a projection-type preamble holds the exact
// header line, it is retained as the first line of the first compiled
// section (the canonical key order's first key), preserving the
// artifact's re-validation and the export -> import -> export round
// trip. Non-projection preambles are dropped entirely.
//
// Returns nil when the artifact carries neither content shape (only
// possible for artifacts whose file had no content at all).
func CompileContentObject(a *Artifact) map[string]any {
	if a.ContentFields != nil {
		return a.ContentFields
	}
	required := RequiredSectionsFor(a.Type)
	fields := map[string]any{}
	currentKey := ""
	var current []string
	var preamble []string
	flush := func() {
		if currentKey != "" {
			fields[currentKey] = strings.TrimSpace(strings.Join(current, "\n"))
		}
		currentKey = ""
		current = nil
	}
	for _, line := range a.BodyLines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "## ") {
			flush()
			heading := strings.TrimSpace(strings.TrimPrefix(t, "## "))
			currentKey = sectionKeyForHeading(heading, required)
			continue
		}
		if currentKey != "" {
			current = append(current, line)
			continue
		}
		preamble = append(preamble, line)
	}
	flush()
	if len(fields) == 0 {
		return nil
	}
	// Projection header retention (documented interpretation above):
	// tkt-/ctr- preambles carrying the exact projection header line keep
	// it as the first line of the first compiled section.
	if (a.Type == "tkt" || a.Type == "ctr") && preambleHasProjectionHeader(preamble) {
		if first := contentKeyOrder(fields, required)[0]; first != "" {
			fields[first] = projectHeader + "\n\n" + fields[first].(string)
		}
	}
	return fields
}

// preambleHasProjectionHeader reports whether the preamble lines contain
// the exact projection header line.
func preambleHasProjectionHeader(preamble []string) bool {
	for _, line := range preamble {
		if strings.TrimRight(strings.TrimSpace(line), "\r") == projectHeader {
			return true
		}
	}
	return false
}

// sectionKeyForHeading maps a Markdown heading to its content key:
// a heading that matches a required section name (headingMatches, the
// v1.1 rule 9 convention — e.g. "## Scope (v2)" counts as Scope) maps to
// the required section's camelCase key; any other heading becomes an
// extra key derived from its own text.
func sectionKeyForHeading(heading string, required []string) string {
	for _, section := range required {
		if headingMatches("## "+heading, section) {
			return SectionKey(section)
		}
	}
	return SectionKey(heading)
}

// ContentJSON encodes the structured content object into the canonical
// compact payload bytes (spec-standard-v2 §3.3): required section keys
// in registry order first, extra keys appended in lexicographic order,
// nested objects with keys sorted lexicographically. Values are encoded
// with encoding/json (strings, nested objects; other JSON values pass
// through). Compact, no trailing newline — the bytes the per-unit
// digest covers.
func ContentJSON(fields map[string]any, required []string) ([]byte, error) {
	keys := contentKeyOrder(fields, required)
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.WriteString(strconv.Quote(k))
		buf.WriteByte(':')
		if err := writeJSONValue(&buf, fields[k]); err != nil {
			return nil, fmt.Errorf("cannot encode content key %q: %w", k, err)
		}
	}
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

// contentKeyOrder is the deterministic key order of the canonical
// encoding: required section keys in registry order (present ones
// only), then the remaining keys in lexicographic order.
func contentKeyOrder(fields map[string]any, required []string) []string {
	seen := map[string]bool{}
	var keys []string
	for _, section := range required {
		k := SectionKey(section)
		if _, ok := fields[k]; ok && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var extras []string
	for k := range fields {
		if !seen[k] {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	return append(keys, extras...)
}

// writeJSONValue encodes one content value: nested objects with sorted
// keys (the canonical rule), everything else with encoding/json.
func writeJSONValue(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.WriteString(strconv.Quote(k))
			buf.WriteByte(':')
			if err := writeJSONValue(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
		return nil
	case string:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
}
