package conformance

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This file implements the filename projection parsing used by Rule 2
// (skeleton/docs/exchange/validation.md Rule 2, ADR-001 §4):
//
//	<type-token>-<id>.md            for non-versioned types
//	<type-token>-<id>-v<nn>.md      for versioned types (scp-, plan-) always
//
// The filename is only a projection of the Identity that lives in the
// frontmatter; it is validated for consistency with the frontmatter, never
// the other way around (ADR-001: "filename divalidasi konsisten dengan
// frontmatter, bukan sebaliknya").
//
// Interpretation (documented): the -v<nn> suffix accepts one or more digits
// (so v1 and v01 both parse), matching the "termasuk v1" requirement in
// docs/README.md. The suffix is matched only at the very end of the base
// name, so an id such as "plan-x-v2" on a versioned type is treated as
// carrying the version 2.

var versionSuffixRe = regexp.MustCompile(`-v([0-9]+)$`)

// parsedFilename is the result of splitting a .md base name (without the
// extension) into its projection parts.
type parsedFilename struct {
	// Token is the type token, i.e. everything before the first dash.
	Token string
	// IDPart is the middle segment of the name, after the token and
	// without any -v<nn> suffix. It is informational only: Rule 2 does
	// NOT require it to equal the frontmatter id (documented gap).
	IDPart string
	// Version is the numeric value of the -v<nn> suffix.
	Version int
	// HasVersion reports whether the name carries a -v<nn> suffix.
	HasVersion bool
}

// parseFilename splits a markdown base name (extension removed).
func parseFilename(name string) (parsedFilename, error) {
	var p parsedFilename
	if name == "" {
		return p, fmt.Errorf("empty filename")
	}
	dash := strings.Index(name, "-")
	if dash < 0 {
		p.Token = name
		return p, nil
	}
	p.Token = name[:dash]
	rest := name[dash+1:]
	if m := versionSuffixRe.FindStringSubmatchIndex(rest); m != nil {
		p.Version, _ = strconv.Atoi(rest[m[2]:m[3]])
		p.HasVersion = true
		rest = rest[:m[0]]
	}
	p.IDPart = rest
	return p, nil
}

// filenameTypeToken extracts the leading type token of a base name, used for
// the Rule 2 consistency check. It never errors; unknown tokens are detected
// by the caller against the type token table.
func filenameTypeToken(name string) string {
	p, _ := parseFilename(name)
	return p.Token
}

// IsJSONAuthoringName reports whether name is a candidate JSON authoring
// file: <type-token>-<id>.json with a known type token — the v2.0 naming
// contract (spec-standard-v2 §3.1, rule 2). It is the scan gateway of the
// JSON authoring adapter: JSON files outside the contract — composer.json,
// package.json, tsconfig.json, lock files, RSF package entries (unit.json,
// manifest.json) — are configuration/foreign files, never EKA authoring,
// and are excluded from every repository scan. Markdown authoring needs no
// such gateway (a .md file is a documentation file by definition; every
// .md is examined and classified, unchanged v1.1 behavior).
func IsJSONAuthoringName(name string) bool {
	base, ok := strings.CutSuffix(name, ".json")
	if !ok {
		return false
	}
	dash := strings.Index(base, "-")
	if dash < 1 || dash == len(base)-1 {
		return false // no "<token>-<id>" split, or an empty id segment
	}
	if _, known := typeTokens[base[:dash]]; !known {
		return false
	}
	return true
}
