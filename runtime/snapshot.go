package runtime

import (
	"github.com/maleolabs/eka-core/exchange"
)

// This file implements the SnapshotService: the verified snapshot read
// side of the Runtime. Writing snapshots happens through the Authoring
// API (Authoring.Sync — push side); this service is the read side:
// verification of a snapshot directory wherever it lives on disk,
// selecting the verification path by layout (exchange.LoadSnapshot —
// source-only snapshots structurally and per-unit, legacy packages
// byte-exact). Single-file .ekapkg containers remain a package concern
// (exchange.LoadPackage) and are not part of the repository transport
// since ADR-027.

// SnapshotService reads and verifies snapshots. Concrete and documented
// — no interface type.
type SnapshotService struct{ rt *Runtime }

// Read opens and verifies the snapshot directory at path — a
// source-only snapshot (header.json, units/, attachments/ — the
// committed transport since ADR-027) or a legacy package carrying the
// derived aggregates — and returns the deserialized model. The
// verification is deterministic: strict JSON decoding (unknown fields
// rejected), per-unit digests recomputed over unit.json || content, and
// (legacy) full package byte-exact verification. Any failure is a
// *exchange.PackageError (rejected package class).
//
// Writing snapshots happens through Authoring.Sync (the push side of
// the sync engine) — this service is read-only.
func (s *SnapshotService) Read(path string) (*exchange.Package, error) {
	res, err := exchange.LoadSnapshot(path)
	if err != nil {
		return nil, err
	}
	return res.Package, nil
}
