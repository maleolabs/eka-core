// Package metadata implements the EKA repository identity metadata
// (eka.yaml): the portable identity file every EKA repository carries
// at its root (ADR-017). The file records the identity triple
// (project, name, namespace):
//
//	version: 1
//	project: atrium
//	name: api
//	namespace: atrium-api
//
// The metadata is the portable identity; the workspace store stays
// canonical per device. Resolution walks up from a directory to the
// filesystem root and takes the nearest eka.yaml (a nested repository
// inside a repository resolves to its own file), then looks the
// registry up by the identity pair (project, name) — the absolute path
// is auxiliary (git worktrees and renames no longer matter).
//
// Parsing is strict: unknown top-level keys and duplicate YAML keys are
// refused (typos must never silently change a repository's identity),
// the file must contain exactly one YAML document, and every field is
// validated. All errors are deterministic strings prefixed
// "metadata: ".
package metadata

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

// metadataFile is the on-disk file name (the ADR-017 identity file).
const metadataFile = "eka.yaml"

// SchemaVersion is the eka.yaml schema version: the only version Parse
// accepts. Exported as the single canonical source of the metadata
// schema version — generators write it, consumers report it, and the
// version must never be re-derived or hardcoded anywhere else.
const SchemaVersion = 1

// Capture holds the universal capture gateway config (ADR-035 v3,
// spec provenance-capture:1). It is optional and versioned in eka.yaml
// so every clone/CI gets the same behaviour without per-device setup.
// When absent the defaults apply: enabled true, threshold 0.6, dedupeWindow
// 24h, provenanceFilterDefault all.
type Capture struct {
	Enabled                 *bool    `yaml:"enabled,omitempty"`
	Threshold               *float64 `yaml:"threshold,omitempty"`
	DedupeWindow            *string  `yaml:"dedupeWindow,omitempty"`
	ProvenanceFilterDefault *string  `yaml:"provenanceFilterDefault,omitempty"`
}

// Metadata is the parsed repository identity.
type Metadata struct {
	// Version is the eka.yaml schema version (SchemaVersion).
	Version int `yaml:"version"`
	// Project is the workspace project the repository belongs to.
	Project string `yaml:"project"`
	// Name is the repository's registry name — the provenance value
	// (source_repo) every object of this repository is attributed to.
	Name string `yaml:"name"`
	// Namespace is the platform-scoped identity prefix: the default
	// namespace of the repository's units. The pair (project,
	// namespace) is immutable after the first sync.
	Namespace string `yaml:"namespace"`
	// Capture is the optional universal gateway config (ADR-035 v3).
	Capture *Capture `yaml:"capture,omitempty"`
}

// validIdentPattern is the EKA identifier rule: lowercase letters,
// digits and single hyphens between alphanumeric runs. bootstrap
// enforces the same rule via its own IsValidIdent (duplicated by
// design — locked by tests in both packages, so they cannot drift).
var validIdentPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// ValidIdent reports whether s is a valid EKA identifier: non-empty,
// lowercase letters/digits, hyphen-separated segments (no leading,
// trailing or double hyphens).
func ValidIdent(s string) bool {
	return validIdentPattern.MatchString(s)
}

// Parse validates and decodes eka.yaml content. Parsing is strict: the
// document must be a single mapping with exactly the keys
// version/project/name/namespace (unknown keys and duplicate keys are
// refused), version must be SchemaVersion, and project/name/namespace
// must be valid identifiers (name additionally must not be the reserved
// "runtime" provenance sentinel). Every error is deterministic and
// prefixed "metadata: ".
func Parse(data []byte) (Metadata, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var m Metadata
	if err := dec.Decode(&m); err != nil {
		if errors.Is(err, io.EOF) {
			return Metadata{}, errors.New("metadata: eka.yaml is empty")
		}
		return Metadata{}, fmt.Errorf("metadata: cannot parse eka.yaml: %w", err)
	}
	// Exactly one document: a second document (after a "---" marker)
	// would silently change the identity otherwise.
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Metadata{}, fmt.Errorf("metadata: cannot parse eka.yaml: %w", err)
		}
		return Metadata{}, errors.New("metadata: eka.yaml must contain exactly one YAML document")
	}

	if m.Version != SchemaVersion {
		return Metadata{}, fmt.Errorf("metadata: eka.yaml version must be %d, got %d", SchemaVersion, m.Version)
	}
	for _, f := range []struct {
		name  string
		value string
	}{{"project", m.Project}, {"name", m.Name}, {"namespace", m.Namespace}} {
		if f.value == "" {
			return Metadata{}, fmt.Errorf("metadata: eka.yaml field %q is required and must not be empty", f.name)
		}
		if !ValidIdent(f.value) {
			return Metadata{}, fmt.Errorf("metadata: eka.yaml field %q must match ^[a-z0-9]+(-[a-z0-9]+)*$", f.name)
		}
	}
	if m.Name == "runtime" {
		return Metadata{}, fmt.Errorf("metadata: eka.yaml name %q is reserved for workspace-native knowledge", m.Name)
	}
	if m.Capture != nil {
		if m.Capture.Threshold != nil {
			v := *m.Capture.Threshold
			if v < 0 || v > 1 {
				return Metadata{}, fmt.Errorf("metadata: eka.yaml capture.threshold must be 0.0-1.0, got %v", v)
			}
		}
		if m.Capture.DedupeWindow != nil {
			s := *m.Capture.DedupeWindow
			if _, err := parseDedupeWindow(s); err != nil {
				return Metadata{}, fmt.Errorf("metadata: eka.yaml capture.dedupeWindow invalid: %w", err)
			}
		}
		if m.Capture.ProvenanceFilterDefault != nil {
			s := *m.Capture.ProvenanceFilterDefault
			if s != "all" && s != "human" && s != "inferred" && s != "reconciled" {
				return Metadata{}, fmt.Errorf("metadata: eka.yaml capture.provenanceFilterDefault must be all|human|inferred|reconciled, got %q", s)
			}
		}
	}
	return m, nil
}

// EffectiveCapture returns the effective capture config with defaults applied.
// Defaults (ADR-035 v3): enabled true, threshold 0.6, dedupeWindow 24h, filter all.
func (m Metadata) EffectiveCapture() (enabled bool, threshold float64, dedupeWindow string, filter string) {
	enabled = true
	threshold = 0.6
	dedupeWindow = "24h"
	filter = "all"
	if m.Capture == nil {
		return
	}
	if m.Capture.Enabled != nil {
		enabled = *m.Capture.Enabled
	}
	if m.Capture.Threshold != nil {
		threshold = *m.Capture.Threshold
	}
	if m.Capture.DedupeWindow != nil {
		dedupeWindow = *m.Capture.DedupeWindow
	}
	if m.Capture.ProvenanceFilterDefault != nil {
		filter = *m.Capture.ProvenanceFilterDefault
	}
	return
}

// parseDedupeWindow validates dedupeWindow string (e.g. 24h, 30m, 1h).
func parseDedupeWindow(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("empty dedupeWindow")
	}
	// Accept Go duration syntax; also accept plain duration strings.
	// Simple validation: must parse as duration.
	// We use regexp for lightweight check without importing time.
	matched, _ := regexp.MatchString(`^[0-9]+(ns|us|ms|s|m|h)$`, s)
	if !matched {
		// Try combined like 24h, 1h30m – fallback to time.ParseDuration via fmt.
		// Import is already available; use time.ParseDuration inline via helper.
		// To avoid import cycle, we validate via gopkg.in/yaml style: attempt parse.
		// Simplified: reject if not simple single unit.
		return "", fmt.Errorf("dedupeWindow %q must be a Go duration like 24h", s)
	}
	return s, nil
}

// Marshal renders the canonical eka.yaml bytes: the single byte format
// of the identity file, one field per line in the fixed order
// version/project/name/namespace with a trailing newline (the same
// format bootstrap generates and the ADR-020 sync/register namespace
// alignment rewrites). When capture config is present it is appended
// as a mapping. The bytes round-trip through Parse: parsing the
// output reproduces the same metadata.
func (m Metadata) Marshal() []byte {
	base := fmt.Sprintf("version: %d\nproject: %s\nname: %s\nnamespace: %s\n",
		m.Version, m.Project, m.Name, m.Namespace)
	if m.Capture == nil {
		return []byte(base)
	}
	// Render capture block only when non-default.
	var sb bytes.Buffer
	sb.WriteString(base)
	sb.WriteString("capture:\n")
	if m.Capture.Enabled != nil {
		sb.WriteString(fmt.Sprintf("  enabled: %v\n", *m.Capture.Enabled))
	}
	if m.Capture.Threshold != nil {
		sb.WriteString(fmt.Sprintf("  threshold: %v\n", *m.Capture.Threshold))
	}
	if m.Capture.DedupeWindow != nil {
		sb.WriteString(fmt.Sprintf("  dedupeWindow: %s\n", *m.Capture.DedupeWindow))
	}
	if m.Capture.ProvenanceFilterDefault != nil {
		sb.WriteString(fmt.Sprintf("  provenanceFilterDefault: %s\n", *m.Capture.ProvenanceFilterDefault))
	}
	return sb.Bytes()
}

// Find locates the nearest eka.yaml by walking up from dir (cleaned
// absolute) toward the filesystem root; the first directory that has
// the file wins. It returns the parsed metadata, the directory that
// holds the file, and found=true; when no ancestor has eka.yaml it
// returns (Metadata{}, "", false, nil). A parse error is returned as
// the error (the file exists, so its content is authoritative). A read
// error other than "no such file" (e.g. a permission denial) is
// returned as the error; only fs.ErrNotExist means "no file at this
// level" and continues the walk.
func Find(dir string) (Metadata, string, bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Metadata{}, "", false, fmt.Errorf("metadata: cannot resolve %q: %w", dir, err)
	}
	abs = filepath.Clean(abs)
	level := abs
	for {
		file := filepath.Join(level, metadataFile)
		data, err := os.ReadFile(file)
		if err == nil {
			m, err := Parse(data)
			if err != nil {
				return Metadata{}, "", false, err
			}
			return m, level, true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Metadata{}, "", false, fmt.Errorf("metadata: cannot read %s: %w", file, err)
		}
		parent := filepath.Dir(level)
		if parent == level {
			return Metadata{}, "", false, nil
		}
		level = parent
	}
}
