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
	return m, nil
}

// Marshal renders the canonical eka.yaml bytes: the single byte format
// of the identity file, one field per line in the fixed order
// version/project/name/namespace with a trailing newline (the same
// format bootstrap generates and the ADR-020 sync/register namespace
// alignment rewrites). The bytes round-trip through Parse: parsing the
// output reproduces the same metadata.
func (m Metadata) Marshal() []byte {
	return []byte(fmt.Sprintf("version: %d\nproject: %s\nname: %s\nnamespace: %s\n",
		m.Version, m.Project, m.Name, m.Namespace))
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
