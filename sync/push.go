package sync

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/maleolabs/eka-core/exchange"
	"github.com/maleolabs/eka-core/metadata"
	"github.com/maleolabs/eka-core/store"
	"github.com/maleolabs/eka-core/workspace"
)

// This file implements the push side of the sync engine: the canonical
// units attributed to a repository (provenance pair project_id +
// source_repo) are read from the workspace store (store.Units: decoded
// from their immutable payloads), and the result is assembled into a
// snapshot and emitted into <repo>/exchange/snapshots.
//
// The snapshot is the SOURCE-ONLY transport since ADR-027:
// exchange.EmitSource projects header.json, units/ and attachments/ —
// the derived aggregates (manifest.json, declarations.json,
// integrity.json) are whole-package projections that would turn every
// parallel Git merge into a synthetic conflict and are therefore not
// committed. The push digest is the deterministic source fingerprint
// (exchange.SnapshotFingerprint) — the idempotent-pull key and the
// sync report's snapshot identity.
//
// Emission is crash-safe: the entries are written into
// <repo>/exchange/.snapshots-tmp first, the existing snapshot directory
// is moved aside to .snapshots-old, the temporary directory is renamed
// into place, and only then is the old copy removed. A failure before
// the final rename leaves the previous snapshot intact (or recoverable
// in .snapshots-old); a failure after the final rename leaves the new
// snapshot in place.
//
// Namespace resolution for the package label (deterministic order):
//  1. a repository with identity metadata (eka.yaml): the REGISTERED
//     repository namespace (repos.namespace, resolved and frozen by
//     `eka sync` from the eka.yaml namespace — a drift between the
//     file and the registry is refused);
//  2. a legacy repository (no eka.yaml): the namespace of an existing
//     snapshot's header, when a snapshot exists;
//  3. else the most common namespace among the repository's units
//     (ties resolve to the lexicographically smallest);
//  4. else an error (a repository with units but no namespace cannot
//     be packaged).
//
// A push with zero stored units is a no-op: nothing is written and
// the result carries empty label/digest.

// PushResult is the outcome of one push run.
type PushResult struct {
	// Units/Attachments count the packaged units/attachments (0 for
	// a no-op push).
	Units       int
	Attachments int
	// Label is the package identity label ("" for a no-op push).
	Label string
	// Digest is the package digest ("" for a no-op push).
	Digest string
	// OldDigest is the digest of the snapshot being replaced ("" when
	// no snapshot existed — a first push).
	OldDigest string
	// Changed reports that the emitted digest differs from the
	// replaced snapshot's digest: the canonical store and the
	// repository snapshot were out of sync, and the push rewrote the
	// snapshot. A tampered store is the typical cause; a clean
	// re-push of identical state reports Changed=false.
	Changed bool
}

// Push assembles the repository's canonical units (store.Units: the
// references resolved to their immutable payloads) into a snapshot
// package at <repo>/exchange/snapshots and records the sync log. A
// repository with no stored units is a no-op (no files written).
func Push(w *workspace.Workspace, repo workspace.Repo) (PushResult, error) {
	units, err := w.Store().Units(repo.ProjectID, repo.Name)
	if err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: %w", err)
	}
	if len(units) == 0 {
		return PushResult{}, nil
	}

	// Namespace resolution: a repository with identity metadata
	// (eka.yaml) uses the registered repository namespace — resolved
	// from eka.yaml by `eka sync` and frozen after the first sync; a
	// legacy repository keeps the snapshot-header/most-common order.
	ns := ""
	m, _, hasMeta, err := metadata.Find(repo.Path)
	if err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: %w", err)
	}
	if hasMeta {
		if repo.Namespace == "" {
			return PushResult{}, fmt.Errorf("sync push failed: the repository has no resolved namespace (run 'eka sync' to register the identity from eka.yaml)")
		}
		if m.Namespace != repo.Namespace {
			return PushResult{}, fmt.Errorf("sync push failed: %w", namespaceImmutableError(m.Namespace, repo.Namespace))
		}
		ns = repo.Namespace
	} else {
		ns, err = resolveNamespace(w, repo, units)
		if err != nil {
			return PushResult{}, fmt.Errorf("sync push failed: %w", err)
		}
	}
	// Persist the resolved namespace as the repository's default
	// (repos.namespace, schema v3): the resolution rule the authoring
	// commands use for unqualified targets inside this repository.
	if err := w.Store().SetRepoNamespace(repo.ProjectID, repo.Name, ns); err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: %w", err)
	}
	label := exchange.PackageIdentityLabel(exchange.ScopeRepository, ns)

	unitSet := map[string]bool{}
	for _, u := range units {
		// Units are sorted by canonical form (store.Units), so the
		// package order is the deterministic canonical identity order.
		unitSet[u.CanonicalIdentityForm] = true
	}

	storedAtts, err := w.Store().Attachments(repo.ProjectID, repo.Name)
	if err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: %w", err)
	}
	attachments := make([]*exchange.Attachment, 0, len(storedAtts))
	for i := range storedAtts {
		a := &storedAtts[i]
		attachments = append(attachments, &exchange.Attachment{ID: a.ID, Digest: a.Digest, Data: a.Data})
	}

	externals := detectExternals(units, unitSet)

	pkg := &exchange.Package{
		Header: exchange.Header{
			SerializationVersion:  exchange.SerializationVersion,
			ExchangeFormatVersion: exchange.ExchangeFormatVersion,
			SpecificationVersion:  exchange.SpecificationVersion,
			Exporter:              exchange.Exporter,
			PackageIdentityLabel:  label,
			ExportScope:           exchange.ScopeRepository,
			Namespace:             ns,
		},
		Units:       units,
		Attachments: attachments,
		Declarations: exchange.Declarations{
			Closure:            exchange.ClosureDeclaration{Scope: exchange.ScopeRepository, Seeds: []string{}},
			ExternalReferences: externals,
			Extensions:         []exchange.ExtensionDecl{},
		},
	}

	files, digest, err := exchange.EmitSource(pkg)
	if err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: %w", err)
	}

	exchangeDir := filepath.Join(repo.Path, "exchange")
	snapshotDir := filepath.Join(exchangeDir, "snapshots")
	tmpDir := filepath.Join(exchangeDir, ".snapshots-tmp")
	oldDir := filepath.Join(exchangeDir, ".snapshots-old")

	// Read the digest of the snapshot being replaced BEFORE the swap:
	// a digest change means the emitted state differs from the current
	// snapshot (e.g. a tampered store), which the sync report must
	// surface instead of claiming "unchanged". The replaced snapshot
	// may be a legacy package (integrity.json carries its package
	// digest) or a source-only snapshot (the fingerprint is computed
	// from the source tree on disk).
	oldDigest := ""
	if data, err := os.ReadFile(filepath.Join(snapshotDir, "integrity.json")); err == nil {
		var integrity exchange.Integrity
		if json.Unmarshal(data, &integrity) == nil {
			oldDigest = integrity.PackageDigest
		}
	} else if existing, err := readSnapshotEntries(snapshotDir); err == nil {
		if fp, err := exchange.SnapshotFingerprint(existing); err == nil {
			oldDigest = fp
		}
	}

	// Stage: write the new snapshot completely into the temporary
	// directory first, so a failure during staging never touches the
	// current snapshot.
	if err := os.RemoveAll(tmpDir); err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: cannot clear staging directory: %w", err)
	}
	for _, f := range files {
		path := filepath.Join(tmpDir, filepath.FromSlash(f.Name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return PushResult{}, fmt.Errorf("sync push failed: cannot create %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, f.Data, 0o644); err != nil {
			return PushResult{}, fmt.Errorf("sync push failed: cannot write %s: %w", f.Name, err)
		}
	}

	// Swap: move the current snapshot aside, move the staged snapshot
	// into place, then drop the old copy. On a rename failure the old
	// snapshot is restored.
	if err := os.RemoveAll(oldDir); err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: cannot clear old-snapshot directory: %w", err)
	}
	if _, err := os.Stat(snapshotDir); err == nil {
		if err := os.Rename(snapshotDir, oldDir); err != nil {
			return PushResult{}, fmt.Errorf("sync push failed: cannot move current snapshot aside: %w", err)
		}
	}
	if err := os.Rename(tmpDir, snapshotDir); err != nil {
		// Restore the previous snapshot (best effort), then report.
		if _, statErr := os.Stat(oldDir); statErr == nil {
			_ = os.Rename(oldDir, snapshotDir)
		}
		return PushResult{}, fmt.Errorf("sync push failed: cannot move snapshot into place: %w", err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		return PushResult{}, fmt.Errorf("sync push failed: cannot remove old snapshot copy: %w", err)
	}

	if err := w.Store().RecordSync(store.SyncEntry{
		ProjectID: repo.ProjectID, Repo: repo.Name, Direction: "push",
		SnapshotDigest: digest, Units: len(units), At: nowUTC(),
	}); err != nil {
		return PushResult{}, err
	}

	return PushResult{
		Units: len(units), Attachments: len(attachments), Label: label, Digest: digest,
		OldDigest: oldDigest, Changed: oldDigest != "" && oldDigest != digest,
	}, nil
}

// resolveNamespace picks the package namespace: the existing
// snapshot's header namespace when a snapshot exists, else the most
// common namespace among the units (ties -> lexicographically
// smallest). An existing but unreadable snapshot is skipped (the units
// carry the authority); a corrupted snapshot header only influences the
// label — the emitted snapshot itself is always rebuilt from the
// canonical store and verified on the next pull.
func resolveNamespace(w *workspace.Workspace, repo workspace.Repo, units []*exchange.Unit) (string, error) {
	snapshotDir := filepath.Join(repo.Path, "exchange", "snapshots")
	if data, err := os.ReadFile(filepath.Join(snapshotDir, "header.json")); err == nil {
		var header struct {
			Namespace string `json:"namespace"`
		}
		if json.Unmarshal(data, &header) == nil && header.Namespace != "" {
			return header.Namespace, nil
		}
	}

	counts := map[string]int{}
	for _, u := range units {
		counts[u.Identity.Namespace]++
	}
	if len(counts) == 0 {
		return "", fmt.Errorf("cannot determine namespace: the repository has no units with a namespace")
	}
	namespaces := make([]string, 0, len(counts))
	for ns := range counts {
		namespaces = append(namespaces, ns)
	}
	sort.Strings(namespaces)
	best, bestCount := namespaces[0], counts[namespaces[0]]
	for _, ns := range namespaces[1:] {
		if counts[ns] > bestCount {
			best, bestCount = ns, counts[ns]
		}
	}
	return best, nil
}

// detectExternals mirrors the export builder's external reference
// detection (builder.go): every relationship target not carried by the
// package, deduplicated and sorted by (source, type, target).
func detectExternals(units []*exchange.Unit, unitSet map[string]bool) []exchange.ExternalReference {
	seen := map[string]bool{}
	var externals []exchange.ExternalReference
	for _, u := range units {
		for _, rel := range u.Relationships {
			if unitSet[rel.Target] {
				continue
			}
			ext := exchange.ExternalReference{Source: u.CanonicalIdentityForm, Type: rel.Type, Target: rel.Target}
			key := ext.Source + "\x00" + ext.Type + "\x00" + ext.Target
			if !seen[key] {
				seen[key] = true
				externals = append(externals, ext)
			}
		}
	}
	sort.Slice(externals, func(i, j int) bool {
		if externals[i].Source != externals[j].Source {
			return externals[i].Source < externals[j].Source
		}
		if externals[i].Type != externals[j].Type {
			return externals[i].Type < externals[j].Type
		}
		return externals[i].Target < externals[j].Target
	})
	return externals
}
