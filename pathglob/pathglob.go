// Package pathglob implements the EKA Standard 1.5 applicability-declaration
// glob grammar (specification/exchange.md, "Content: applicability
// declaration (optional)"): repository-relative, forward-slashed on every
// platform, anchored at the repository root.
//
//   - matches within a single path segment (never crosses "/")
//     ?   matches exactly one character (never "/")
//     **  matches across segments, including zero segments
//
// Every other character is literal: "[", "]", "{", "}" carry no special
// meaning, and there is no escape character. A pattern is a literal path or
// a glob in this restricted grammar.
//
// The package is a leaf (standard library only): the conformance validator
// (R0 content-shape check) and the contexts retrieval door both consume it,
// so the definition of "malformed" is written once.
package pathglob

import (
	"errors"
	"fmt"
	"strings"
)

// Pattern is one compiled applicability pattern.
type Pattern struct {
	src      string
	segments []string
}

// String returns the source pattern text.
func (p Pattern) String() string { return p.src }

// Compile interprets one pattern. It refuses an empty pattern and a "**"
// that is not a whole path segment (e.g. "a**b"), because such a pattern
// cannot be interpreted deterministically — the "**" usage is ambiguous.
func Compile(pattern string) (Pattern, error) {
	if pattern == "" {
		return Pattern{}, errors.New("empty pattern")
	}
	segments := strings.Split(pattern, "/")
	for _, seg := range segments {
		if strings.Contains(seg, "**") && seg != "**" {
			return Pattern{}, fmt.Errorf("\"**\" must be a whole path segment (found in %q)", seg)
		}
	}
	return Pattern{src: pattern, segments: segments}, nil
}

// Interpret compiles the decoded value of an `appliesTo` content field — a
// JSON array of pattern strings — into patterns. It is the single source of
// truth for what makes a declaration malformed: a value that is not an
// array, an element that is not a string, or a pattern that cannot be
// compiled. An absent field is never passed here (absence is not a defect).
func Interpret(v any) ([]Pattern, error) {
	var items []any
	switch t := v.(type) {
	case []any:
		items = t
	case []string:
		items = make([]any, len(t))
		for i, s := range t {
			items[i] = s
		}
	default:
		return nil, errors.New("must be an array of path patterns")
	}
	out := make([]Pattern, 0, len(items))
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("element %d must be a string", i)
		}
		p, err := Compile(s)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %w", s, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// Match reports whether path (repository-relative, forward-slashed) is
// matched in full by the pattern — the whole path must match, not a prefix.
func (p Pattern) Match(path string) bool {
	return matchSegments(p.segments, strings.Split(path, "/"))
}

// matchSegments matches a pattern-segment list against a path-segment list.
// A "**" segment matches zero or more path segments; consecutive "**"
// segments collapse (they are equivalent to one).
func matchSegments(pattern, path []string) bool {
	for len(pattern) > 0 {
		if pattern[0] == "**" {
			rest := pattern[1:]
			for len(rest) > 0 && rest[0] == "**" {
				rest = rest[1:]
			}
			if len(rest) == 0 {
				return true // "**" at the end: everything below matches.
			}
			for i := 0; i <= len(path); i++ {
				if matchSegments(rest, path[i:]) {
					return true
				}
			}
			return false
		}
		if len(path) == 0 || !matchSegment(pattern[0], path[0]) {
			return false
		}
		pattern, path = pattern[1:], path[1:]
	}
	return len(path) == 0
}

// matchSegment matches one path segment (no "/" inside) with "*" matching a
// run of characters, "?" exactly one character, and every other character
// literal. Matching is over runes so "?" counts one character, not one byte.
func matchSegment(pattern, name string) bool {
	p := []rune(pattern)
	s := []rune(name)
	pi, si := 0, 0
	star, mark := -1, 0
	for si < len(s) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == s[si]):
			pi++
			si++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, si
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			si = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}
