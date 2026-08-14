package runtime

import (
	"github.com/maleolabs/eka-core/store"
)

// This file implements the IntegrityService: the store integrity
// verification of the Runtime Kernel. SQLite is a persistence layer,
// not a trust boundary — verification recomputes every content-derived
// value and compares it with the stored state, so manual database
// modification is DETECTED (it is never prevented).

// IntegrityReport is the outcome of one verification run (re-exported
// store contract type): scanned counts, retained-history orphans and
// the deterministic violation list.
type IntegrityReport = store.IntegrityReport

// IntegrityService verifies the canonical store and reports the schema
// version. Concrete and documented — no interface type.
type IntegrityService struct{ rt *Runtime }

// Verify scans the whole canonical store and reports every integrity
// violation (payload hashes, payload decoding, reference targets,
// reference index columns, attachment digests, registry). The scan is
// read-only; violations are sorted by (Kind, Subject). Unreferenced
// payloads are the immutable history archive — counted, never
// violations.
func (s *IntegrityService) Verify() (*IntegrityReport, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return nil, err
	}
	return st.VerifyIntegrity()
}

// SchemaVersion returns the canonical store schema version (eka.db) —
// the meaningful schema of the Runtime (the workspace.json file format
// version is an internal detail).
func (s *IntegrityService) SchemaVersion() (int, error) {
	st, err := s.rt.requireStore()
	if err != nil {
		return 0, err
	}
	return st.SchemaVersion()
}
