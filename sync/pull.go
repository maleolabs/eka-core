package sync

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/maleolabs/eka-core/compile"
	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the pull side of the sync engine: snapshot mode
// (an existing <repo>/exchange/snapshots package is verified and its
// units seeded into the canonical store) and docs mode (the docs tree
// is validated and seeded — the migration path and the --from-docs
// re-seed).
//
// Units are seeded as IMMUTABLE payloads (store.PutUnit): the canonical
// unit.json bytes (raw package entry in snapshot mode, exchange.MarshalUnit
// in docs mode) plus the content bytes, hashed content-addressed; the
// reference (resolver key = canonical identity form) is the only mutable
// state and is upserted with the repository provenance pair
// (project_id, source_repo).
//
// Idempotency: when the last recorded pull digest equals the current
// snapshot digest the seed work is skipped (report "unchanged"). The
// package is still verified first — a corrupt snapshot is always an
// error, never silently skipped. Re-seeding the same package is a
// no-op: PutUnit returns the same hash and the reference upsert is
// idempotent.
//
// Deletions are never applied: a unit missing from a new pull simply
// stays in the canonical store (its payload remains in the history
// archive).
//
// Cross-repository conflicts: when a pull seeds an identity already
// referenced by a different repository (different provenance pair) with
// different content, the new value wins (deterministic last-wins) and
// the overwrite is recorded in the pull result, surfaced as a sync
// warning. The same identity pulled again from its own repository is a
// plain refresh, never a conflict.

// PullResult is the outcome of one pull run.
type PullResult struct {
	// Source is "snapshot" or "docs".
	Source string
	// Units/Attachments count the seeded units/attachments (0 for
	// an unchanged pull).
	Units       int
	Attachments int
	// Digest is the package digest of the pulled package ("" when no
	// package was produced).
	Digest string
	// Unchanged reports an idempotent pull (digest already current).
	Unchanged bool
	// Overwrites lists the identities this pull replaced from another
	// repository (deterministic order, empty when none).
	Overwrites []string
	// KeptNewer lists the identities whose reference was NOT re-pointed
	// by this pull because the canonical store already resolves a
	// NEWER instance version (forward-only reference guard — the pull
	// re-seeded an older instance; deterministic order, empty when
	// none).
	KeptNewer []string
}

// Pull seeds the canonical store from the repository's knowledge: the
// snapshot directory when it exists (and fromDocs is false), else the
// docs tree. The returned digest is the package digest of the pulled
// snapshot (snapshot mode) or of the docs-assembled package (docs
// mode).
func Pull(w *workspace.Workspace, repo workspace.Repo, fromDocs bool) (PullResult, error) {
	snapshotDir := filepath.Join(repo.Path, "exchange", "snapshots")

	if !fromDocs {
		if info, err := os.Stat(snapshotDir); err == nil && info.IsDir() {
			return pullSnapshot(w, repo, snapshotDir)
		}
	}
	return pullDocs(w, repo)
}

// pullSnapshot verifies the snapshot package and seeds its units and
// attachments into the canonical store. The verification path is
// selected by the snapshot layout (exchange.LoadSnapshot): a source-only
// snapshot (the committed transport since ADR-027 — no derived
// aggregates) is verified structurally and per-unit; a legacy snapshot
// carrying the aggregates is verified byte-exact as before. A corrupt
// or refused snapshot is an error (integrity failure class, exit 1);
// an unchanged digest skips the seed work.
func pullSnapshot(w *workspace.Workspace, repo workspace.Repo, snapshotDir string) (PullResult, error) {
	res, err := exchange.LoadSnapshot(snapshotDir)
	if err != nil {
		return PullResult{}, fmt.Errorf("sync pull failed: snapshot package refused: %w", err)
	}
	pkg := res.Package
	entries := res.Entries
	digest := res.Digest

	last, ok, err := w.Store().LastPullDigest(repo.ProjectID, repo.Name)
	if err != nil {
		return PullResult{}, err
	}
	if ok && last == digest {
		// Idempotent: verified above, no work needed.
		return PullResult{Source: "snapshot", Digest: digest, Unchanged: true}, nil
	}

	overwrites, keptNewer, err := upsertPackage(w, repo, pkg, entries)
	if err != nil {
		return PullResult{}, err
	}

	if err := w.Store().RecordSync(store.SyncEntry{
		ProjectID: repo.ProjectID, Repo: repo.Name, Direction: "pull",
		SnapshotDigest: digest, Units: len(pkg.Units), At: nowUTC(),
	}); err != nil {
		return PullResult{}, err
	}
	return PullResult{
		Source: "snapshot", Units: len(pkg.Units), Attachments: len(pkg.Attachments),
		Digest: digest, Overwrites: overwrites, KeptNewer: keptNewer,
	}, nil
}

// pullDocs compiles the repository through the Knowledge Compiler
// (conformance gate + package assembly) and seeds the canonical store
// from its docs tree: the package is assembled exactly as `eka export`
// would assemble it (exchange.RepositoryPackage, inside compile.Compile),
// and its units and attachments are seeded. The reported digest is the
// SOURCE FINGERPRINT of the assembled package (exchange.EmitSource) —
// the same digest a push of the seeded store produces, so a docs-mode
// pull followed by a snapshot pull reports "unchanged". source is
// reported as "docs".
func pullDocs(w *workspace.Workspace, repo workspace.Repo) (PullResult, error) {
	result, err := compile.Compile(repo.Path)
	if err != nil {
		var ve *compile.ValidationError
		if errors.As(err, &ve) {
			return PullResult{}, ve
		}
		return PullResult{}, fmt.Errorf("sync pull failed: cannot compile repository: %w", err)
	}
	pkg := result.Package
	_, digest, err := exchange.EmitSource(pkg)
	if err != nil {
		return PullResult{}, fmt.Errorf("sync pull failed: cannot fingerprint the assembled package: %w", err)
	}

	// Docs mode has no raw package entries: the unit.json bytes are
	// serialized per unit (identical to the bytes RepositoryPackage's
	// assembly hashed into u.Digest).
	overwrites, keptNewer, err := upsertPackage(w, repo, pkg, nil)
	if err != nil {
		return PullResult{}, err
	}

	if err := w.Store().RecordSync(store.SyncEntry{
		ProjectID: repo.ProjectID, Repo: repo.Name, Direction: "pull",
		SnapshotDigest: digest, Units: len(pkg.Units), At: nowUTC(),
	}); err != nil {
		return PullResult{}, err
	}
	return PullResult{
		Source: "docs", Units: len(pkg.Units), Attachments: len(pkg.Attachments),
		Digest: digest, Overwrites: overwrites, KeptNewer: keptNewer,
	}, nil
}

// upsertPackage seeds every unit and attachment of the package into the
// canonical store, attributed to repo. It returns the deterministic
// list of identities this pull overwrote from a different repository
// and the deterministic list of identities whose reference was KEPT at
// a newer instance (forward-only reference guard: the pull re-seeded
// an older instance version, so the reference was not re-pointed).
//
// entries carries the package's raw entry bytes (snapshot mode); when
// nil (docs mode) each unit's unit.json is serialized via
// exchange.MarshalUnit — byte-identical to what the package digest
// covers. Either way the stored hash must equal the unit's package
// digest (LoadPackage already verified it in snapshot mode; the
// equality is asserted as a defense in depth).
func upsertPackage(w *workspace.Workspace, repo workspace.Repo, pkg *exchange.Package, entries map[string][]byte) ([]string, []string, error) {
	var overwrites []string
	var keptNewerList []string
	for _, u := range pkg.Units {
		unitJSON, err := unitJSONFor(u, entries)
		if err != nil {
			return nil, nil, fmt.Errorf("sync pull failed: %w", err)
		}

		// Cross-repository conflict detection: the identity already
		// exists from a different provenance pair with different
		// content — deterministic last-wins, recorded.
		if existing, ok, err := w.Store().Ref(u.CanonicalIdentityForm); err != nil {
			return nil, nil, fmt.Errorf("sync pull failed: %w", err)
		} else if ok &&
			(existing.ProjectID != repo.ProjectID || existing.SourceRepo != repo.Name) &&
			existing.ObjectHash != u.Digest {
			overwrites = append(overwrites, fmt.Sprintf(
				"%s: overwrote from %s/%s", u.CanonicalIdentityForm, existing.ProjectID, existing.SourceRepo))
		}

		hash, keptNewer, err := w.Store().PutUnit(unitJSON, u.ContentPayload, refFromUnit(u, repo))
		if err != nil {
			return nil, nil, fmt.Errorf("sync pull failed: %w", err)
		}
		if hash != u.Digest {
			return nil, nil, fmt.Errorf(
				"sync pull failed: stored hash of %s (%s) does not match the package digest %s",
				u.CanonicalIdentityForm, hash, u.Digest)
		}
		if keptNewer {
			keptNewerList = append(keptNewerList, fmt.Sprintf(
				"%s: kept newer instance (pulled version older than the referenced one; reference unchanged)", u.CanonicalIdentityForm))
		}
	}
	sort.Strings(overwrites)
	sort.Strings(keptNewerList)

	for _, a := range pkg.Attachments {
		if err := w.Store().UpsertAttachment(repo.ProjectID, repo.Name, a.ID, a.Digest, a.Data); err != nil {
			return nil, nil, fmt.Errorf("sync pull failed: %w", err)
		}
	}
	return overwrites, keptNewerList, nil
}

// unitJSONFor returns the canonical unit.json bytes of one unit: the
// raw package entry when the entry map is available (snapshot mode —
// byte-exact, so the stored payload hash equals the package digest),
// else the serializer output (docs mode).
func unitJSONFor(u *exchange.Unit, entries map[string][]byte) ([]byte, error) {
	if entries != nil {
		data, ok := entries[exchange.UnitDirName(u.Identity)+"/unit.json"]
		if !ok {
			return nil, fmt.Errorf("sync pull failed: package is missing the unit.json entry of %s", u.CanonicalIdentityForm)
		}
		return data, nil
	}
	data, err := exchange.MarshalUnit(u)
	if err != nil {
		return nil, fmt.Errorf("sync pull failed: cannot serialize %s: %w", u.CanonicalIdentityForm, err)
	}
	return data, nil
}

// refFromUnit projects one exchange unit onto the reference it points
// at, attributed to repo. The index columns are derived from the
// payload; UpdatedAt is runtime bookkeeping (never inside payload
// bytes).
func refFromUnit(u *exchange.Unit, repo workspace.Repo) store.Ref {
	return store.Ref{
		Form:            u.CanonicalIdentityForm,
		ProjectID:       repo.ProjectID,
		SourceRepo:      repo.Name,
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Dimension:       u.Classification.Dimension,
		Domain:          u.Classification.Domain,
		Phase:           u.Phase,
		UpdatedAt:       nowUTC(),
	}
}

// nowUTC renders the current time as a deterministic UTC RFC3339
// timestamp (sync log bookkeeping only — never inside package bytes).
func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}
