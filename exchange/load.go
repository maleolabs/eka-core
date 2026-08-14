package exchange

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/maleolabs/eka-core/conformance"
)

// This file implements the knowledge loader: it turns the scanned source
// repository into the working set (artifacts + content payloads + attachment
// payloads) from which the builder selects units.
//
// Content compilation (spec-standard-v2 §6): the loader is the
// repository side of the compiler. Every artifact's content is compiled
// into the canonical content object and encoded with the canonical
// compact payload encoding (§3.3):
//
//	JSON-native artifacts:  the structured content object is taken
//	                        directly from the authoring file;
//	Markdown artifacts:     the body is compiled — sections split on
//	                        "## " headings, mapped to camelCase keys
//	                        (SectionKey), preamble dropped.
//
// The compiled payload is the eka/structured-json/1 content of the unit
// (legacy eka/structured-text/1 payloads remain readable at import).
//
// Attachment policy (same exclusion policy as the validator, documented in
// conformance/doc.go): every non-.md non-.json file under docs/ is an
// attachment candidate; directories named "testdata" and dot-directories
// are not descended into; symlinks are skipped (WalkDir never follows
// them, but entries that ARE symlinks are skipped explicitly so a
// symlinked file is never carried). The Attachment ID is the
// repository-relative path with forward slashes — deterministic (RSF
// §7.2).

// loadedRepo is the working set of one export run.
type loadedRepo struct {
	// root is the repository root as given.
	root string
	// artifacts are the classified artifacts in scan order.
	artifacts []conformance.Artifact
	// attachments maps Attachment ID (relative path, forward slashes) ->
	// payload bytes. v1 carries no unit references to attachments: orphan
	// attachments are allowed (RSF §7.1; documented).
	attachments map[string][]byte
	// byLine indexes artifacts by line key (namespace, type, id); each
	// bucket holds all instances sorted by instance-version (ascending).
	byLine map[string][]*loadedArtifact
	// instanceByForm maps canonical identity form -> loaded artifact.
	instanceByForm map[string]*loadedArtifact
}

// loadedArtifact is a conformance artifact plus its content payload and a
// back-reference to the repository index for reference resolution.
type loadedArtifact struct {
	conformance.Artifact
	// content is the byte-exact markdown body after the frontmatter.
	content []byte
	// repo is the owning loadedRepo (reference resolution).
	repo *loadedRepo
}

// IdentityForm renders the RSF Canonical Identity Form (RSF §5.2).
func (la *loadedArtifact) IdentityForm() string {
	return la.Namespace + "/" + la.Type + ":" + la.ID + ":" + strconv.Itoa(la.InstanceVersion)
}

// instances returns every loaded artifact once, in canonical identity
// order. Deterministic across runs.
func (r *loadedRepo) instances() []*loadedArtifact {
	out := make([]*loadedArtifact, 0, len(r.artifacts))
	for _, bucket := range r.byLine {
		out = append(out, bucket...)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].IdentityForm() < out[j].IdentityForm()
	})
	return out
}

// identityLineKey builds the line key (Namespace, Type, ID).
func identityLineKey(ns, typeToken, id string) string {
	return ns + "\x00" + typeToken + "\x00" + id
}

// load reads the repository into the working set. Every artifact identity
// passes the charset guard (validateIdentityComponent) before anything is
// built or written: identity components become package entry path segments,
// so an unconstrained component would be a path-traversal vector.
func load(root string) (*loadedRepo, error) {
	artifacts, err := conformance.Scan(root)
	if err != nil {
		return nil, fmt.Errorf("load failed: %w", err)
	}
	r := &loadedRepo{
		root:           root,
		attachments:    map[string][]byte{},
		byLine:         map[string][]*loadedArtifact{},
		instanceByForm: map[string]*loadedArtifact{},
	}
	for i := range artifacts {
		la := &loadedArtifact{Artifact: artifacts[i], repo: r}
		form := la.IdentityForm()
		for _, c := range []struct{ component, label string }{
			{la.Namespace, "namespace"},
			{la.Type, "type"},
			{la.ID, "id"},
		} {
			if err := validateIdentityComponent(c.component, c.label, form); err != nil {
				return nil, err
			}
		}
		// Content compilation: the canonical content object (JSON-native
		// artifacts carry it directly; Markdown bodies are compiled into
		// it), encoded with the canonical compact payload encoding. The
		// unit's content is always eka/structured-json/1 — identical
		// knowledge always yields identical payload bytes (§3.3).
		payload, err := compileContentPayload(&la.Artifact)
		if err != nil {
			return nil, fmt.Errorf("cannot compile content of %s: %w", la.RelPath, err)
		}
		la.content = payload
		r.artifacts = append(r.artifacts, la.Artifact)
		lineKey := identityLineKey(la.Namespace, la.Type, la.ID)
		r.byLine[lineKey] = append(r.byLine[lineKey], la)
		r.instanceByForm[la.IdentityForm()] = la
	}
	for _, bucket := range r.byLine {
		sort.Slice(bucket, func(i, j int) bool {
			return bucket[i].InstanceVersion < bucket[j].InstanceVersion
		})
	}

	if err := r.loadAttachments(); err != nil {
		return nil, err
	}
	return r, nil
}

// validateIdentityComponent enforces the RSF §5.2.3 component charset on
// one identity component (Namespace, Type or ID) of one artifact. Each
// component becomes a path segment in the package entries
// ("units/<namespace>/<type>-<id>-v<nn>"), so an unconstrained component
// would let a package entry escape the package root (directory-mode
// traversal / zip-slip). Rejected: empty, "/", ":", whitespace, backslash,
// "." and "..", anything containing "..", and leading/trailing dots. On
// violation a deterministic *ContentError naming the artifact (canonical
// identity form) and the offending component is returned.
func validateIdentityComponent(component, label, artifact string) error {
	switch {
	case component == "":
		return contentErrorf(
			"export refused: empty %s on artifact %s violates the identity charset (RSF §5.2.3)",
			label, artifact)
	case component == "." || component == ".." ||
		strings.ContainsAny(component, "/:\\") ||
		strings.Contains(component, "..") ||
		hasWhitespace(component):
		return contentErrorf(
			"export refused: %s %q on artifact %s violates the identity charset (RSF §5.2.3): must not contain '/', ':', '\\', whitespace, or '..'",
			label, component, artifact)
	case strings.HasPrefix(component, ".") || strings.HasSuffix(component, "."):
		return contentErrorf(
			"export refused: %s %q on artifact %s violates the identity charset (RSF §5.2.3): must not start or end with '.'",
			label, component, artifact)
	}
	return nil
}

// hasWhitespace reports whether s contains any Unicode whitespace.
func hasWhitespace(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

// extractBody returns the bytes after the frontmatter block, mirroring the
// validator's frontmatter detection exactly (first line "---", next line
// "---", CR-tolerant). Because analyzeFile splits on "\n" and the body is
// the lines after the closing delimiter, joining those lines with "\n"
// reproduces the body byte-exactly for any line-ending convention.
func extractBody(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	if strings.TrimRight(lines[0], "\r") != "---" {
		return data // No frontmatter block: convention document, unused.
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r") == "---" {
			closeIdx = i
			break
		}
	}
	if closeIdx < 0 {
		return data // Unterminated block: the validator reports it (R0).
	}
	return []byte(strings.Join(lines[closeIdx+1:], "\n"))
}

// compileContentPayload renders the canonical compact content payload of
// one artifact (spec-standard-v2 §3.3): the compiled content object
// encoded with required-section keys first, extra keys lexicographic,
// nested objects sorted. Empty content (no sections, no fields) encodes
// as "{}".
func compileContentPayload(a *conformance.Artifact) ([]byte, error) {
	fields := conformance.CompileContentObject(a)
	required := conformance.RequiredSectionsFor(a.Type)
	if fields == nil {
		fields = map[string]any{}
	}
	return conformance.ContentJSON(fields, required)
}

// loadAttachments walks root/docs and collects every non-.md file under
// the shared exclusion policy.
func (r *loadedRepo) loadAttachments() error {
	docsDir := filepath.Join(r.root, "docs")
	info, err := os.Stat(docsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No docs/: no attachments.
		}
		return fmt.Errorf("cannot access docs/: %w", err)
	}
	if !info.IsDir() {
		return nil // docs/ exists as a file: not a repository docs tree.
	}

	err = filepath.WalkDir(docsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != docsDir {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "testdata" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil // Symlinks are never carried.
		}
		if strings.HasSuffix(d.Name(), ".md") || strings.HasSuffix(d.Name(), ".json") {
			return nil // Authoring files (.md legacy, .json v2.0-native) are artifacts, not attachments.
		}
		rel, err := filepath.Rel(r.root, path)
		if err != nil {
			return err
		}
		id := filepath.ToSlash(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read attachment %s: %w", id, err)
		}
		r.attachments[id] = data
		return nil
	})
	if err != nil {
		return fmt.Errorf("attachment scan failed: %w", err)
	}
	return nil
}

// resolveRef resolves a reference against the repository index using the
// validator's resolution semantics (conformance rule 5): a versioned
// reference names the exact instance; a line reference resolves to the
// lowest instance of the line. Returns nil when the target does not exist
// (possible only for draft artifacts — the validation gate would have
// blocked it otherwise).
func (r *loadedRepo) resolveRef(ref conformance.Reference) *loadedArtifact {
	bucket := r.byLine[identityLineKey(ref.Namespace, ref.Type, ref.ID)]
	if len(bucket) == 0 {
		return nil
	}
	if ref.HasVersion {
		for _, a := range bucket {
			if a.InstanceVersion == ref.Version {
				return a
			}
		}
		return nil
	}
	return bucket[0] // bucket is sorted by instance-version ascending.
}
