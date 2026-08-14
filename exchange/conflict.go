package exchange

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// This file implements the import conflict and duplicate detection
// (Exchange §11.1 phases 6-7, §11.2 conflict policy, §13.2 merge behavior).
//
// v1 integration strategy: CONSERVATIVE MERGE (design decision 3). Only
// NEW artifacts are written; a unit whose identity is already present in
// the target repository is either a duplicate (identical payload -> no-op,
// phase 7) or a conflict (any difference -> reject, phase 6). No
// overwrite, no delete, no replace strategy exists in v1; in particular
// the Exchange §13.2 "forward-only state reconciliation" (accepting
// imported state that is reachable forward from the target value) is NOT
// implemented: any payload difference is a conflict. Documented deviation:
// conservative by design, deterministic for identical inputs.
//
// Payload comparison is the full unit composition (Exchange §10.3): the
// repository side is projected through the exact exporter mapping (toUnit),
// so the comparison is apples-to-apples. Differences are reported
// per-domain in a deterministic order, so the conflict summary is stable.

// ConflictDetail describes one conflicting artifact: the identity (or
// attachment ID) and the deterministic list of differing aspects.
type ConflictDetail struct {
	// Identity is the canonical identity form (units) or the attachment
	// ID.
	Identity string
	// Differences lists the differing payload aspects, deterministically
	// ordered.
	Differences []string
}

// conflictSummary renders one conflict detail as a single deterministic
// line.
func (c ConflictDetail) String() string {
	return c.Identity + ": " + strings.Join(c.Differences, "; ")
}

// ConflictError reports that the import was refused because package
// content conflicts with existing repository content (Exchange §11.2
// reject-by-default). Mapped to exit code 1 by the CLI.
type ConflictError struct {
	// Conflicts lists every conflicting artifact, sorted by identity.
	Conflicts []ConflictDetail
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("import refused: %d conflict(s); no changes written (reject-by-default, Exchange §11.2)",
		len(e.Conflicts))
}

// classification groups the package content of one import run into the
// phase 6/7 verdicts.
type classification struct {
	// newUnits holds the accepted new units in canonical identity order.
	newUnits []*Unit
	// duplicateForms lists the no-op duplicate identities (sorted).
	duplicateForms []string
	// conflicts lists the rejected identities with their differences.
	conflicts []ConflictDetail
	// attachments: new IDs, duplicate IDs, conflicting IDs (sorted).
	newAttachments      []*Attachment
	duplicateAttachment []string
	conflictAttachment  []ConflictDetail
}

// classify compares every package unit and attachment against the target
// repository and produces the deterministic verdicts (phases 6-7).
func classify(pkg *loadedPackage, target *IdentityResolver) *classification {
	c := &classification{}

	for _, u := range pkg.units {
		repoUnit := target.repoUnit(u.CanonicalIdentityForm)
		if repoUnit == nil {
			c.newUnits = append(c.newUnits, u)
			continue
		}
		diffs := unitDifferences(u, repoUnit)
		if len(diffs) == 0 {
			c.duplicateForms = append(c.duplicateForms, u.CanonicalIdentityForm)
			continue
		}
		c.conflicts = append(c.conflicts, ConflictDetail{
			Identity:    u.CanonicalIdentityForm,
			Differences: diffs,
		})
	}

	for _, a := range pkg.attachments {
		path := filepath.Join(target.repo.root, filepath.FromSlash(a.ID))
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				c.newAttachments = append(c.newAttachments, a)
				continue
			}
			// Unreadable existing file: treat as a conflict (conservative).
			c.conflictAttachment = append(c.conflictAttachment, ConflictDetail{
				Identity:    a.ID,
				Differences: []string{"target file exists but cannot be read"},
			})
			continue
		}
		if bytes.Equal(data, a.Data) {
			c.duplicateAttachment = append(c.duplicateAttachment, a.ID)
			continue
		}
		c.conflictAttachment = append(c.conflictAttachment, ConflictDetail{
			Identity:    a.ID,
			Differences: []string{"attachment content differs from the existing target file"},
		})
	}

	sort.Slice(c.conflicts, func(i, j int) bool { return c.conflicts[i].Identity < c.conflicts[j].Identity })
	sort.Slice(c.conflictAttachment, func(i, j int) bool {
		return c.conflictAttachment[i].Identity < c.conflictAttachment[j].Identity
	})
	return c
}

// unitDifferences compares a package unit against the projected repository
// unit and returns the deterministic difference list (empty = identical).
func unitDifferences(pkgUnit, repoUnit *Unit) []string {
	var diffs []string
	add := func(d string) { diffs = append(diffs, d) }

	if !bytes.Equal(pkgUnit.ContentPayload, repoUnit.ContentPayload) {
		add("content differs")
	}
	if pkgUnit.StateVector != repoUnit.StateVector {
		var domains []string
		for _, d := range []struct{ field, label string }{
			{pkgUnit.StateVector.ContentState, "content-state"},
			{pkgUnit.StateVector.ExecutionState, "execution-state"},
			{pkgUnit.StateVector.PlanningState, "planning-state"},
			{pkgUnit.StateVector.ContainerState, "container-state"},
			{pkgUnit.StateVector.ExistenceState, "existence-state"},
			{pkgUnit.StateVector.NoteState, "note-state"},
		} {
			if d.field != "" {
				domains = append(domains, d.label)
			}
		}
		sort.Strings(domains)
		if len(domains) == 0 {
			add("state vector differs")
		} else {
			add("state differs (domains: " + strings.Join(domains, ", ") + ")")
		}
	}
	if !changeLogsEqual(pkgUnit.ChangeLog, repoUnit.ChangeLog) {
		add("change log differs")
	}
	if !relationshipsEqual(pkgUnit.Relationships, repoUnit.Relationships) {
		add("relationships differ")
	}
	if !classificationsEqual(pkgUnit.Classification, repoUnit.Classification) {
		add("classification differs")
	}
	if pkgUnit.Phase != repoUnit.Phase {
		add("phase differs")
	}
	// Revision and author/created/updated are unit metadata (Exchange
	// §6.4): a difference is still a conflict (conservative policy,
	// documented — identical payload with different metadata is rejected
	// rather than silently adopted).
	if pkgUnit.Revision != repoUnit.Revision {
		add("revision differs")
	}
	if pkgUnit.Author != repoUnit.Author || pkgUnit.Created != repoUnit.Created || pkgUnit.Updated != repoUnit.Updated {
		add("metadata differs (author/created/updated)")
	}
	return diffs
}

// changeLogsEqual compares two change logs entry-by-entry in order.
func changeLogsEqual(a, b []ChangeLogEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// relationshipsEqual compares two relationship lists as (type, target)
// sets; both sides are sorted by (type, target) (RSF §6.3, Exchange §10.5).
func relationshipsEqual(a, b []Relationship) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// classificationsEqual compares two classification tuples (the secondary
// dimension list prevents struct comparison).
func classificationsEqual(a, b Classification) bool {
	if a.Dimension != b.Dimension {
		return false
	}
	if len(a.DimensionsSecondary) != len(b.DimensionsSecondary) {
		return false
	}
	for i := range a.DimensionsSecondary {
		if a.DimensionsSecondary[i] != b.DimensionsSecondary[i] {
			return false
		}
	}
	return true
}
