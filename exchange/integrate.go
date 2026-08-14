package exchange

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/maleolabs/eka-core/conformance"
	"gopkg.in/yaml.v3"
)

// This file implements the import commit side (Exchange §11.1 phases 8-10,
// design decisions 5-7): dependency-resolved commit order, the
// deterministic write plan (repository serialization: frontmatter
// reconstruction + folder/filename mapping), the staged repository writer
// (temp file + atomic rename per file) and the rollback on any failure.
//
// Repository serialization (validation.md Aturan 1-9; design decision 5):
//
//	filename: <type-token>-<id>[-v<nn>].md/.json
//	           (.md legacy for structured-text units, .json v2.0-native
//	           for structured-json units — spec-standard-v2 §3.1)
//	folder:   mapped from the type token (family table below)
//	content:  YAML frontmatter (fixed field order, gopkg.in/yaml.v3 struct
//	          marshaling — deterministic) + body byte-exact from the
//	          content payload (legacy .md), or the full §3.2 JSON authoring
//	          document (2-space indent, trailing newline) for .json.
//
// The frontmatter carries exactly the fields the exporter reads back
// (load.go, conformance/artifact.go): namespace, type, id,
// instance-version, revision, owned state fields, dimension (+
// dimensions-secondary), author, created, updated, relationship fields
// (present only when non-empty), phase (scp-/plan-), change-log list.
//
// Round-trip note (documented): the reconstructed legacy .md file is
//
//	"---\n" + frontmatter + "---\n" + payload
//
// — the closing delimiter is followed directly by the payload's first
// bytes. extractBody (load.go) returns everything after the closing
// delimiter, so any extra "\n" inserted here would corrupt payloads that
// begin with a blank line (the convention of every repository fixture).
// This keeps export -> import -> export byte-identical.
//
// JSON round-trip: the .json document's content object is the canonical
// payload re-indented (json.Indent over the emitted document — the
// authoring file is parsed, not copied, so its key order is irrelevant;
// the compiler re-emits the canonical payload on the next export).

// folderForType maps a type token to its repository folder (design
// decision 5; the dimension token of the folder equals the artifact's
// dimension for knowledge artifacts — R6 compliance by construction).
func folderForType(token string) string {
	switch token {
	case "vis", "str":
		return "docs/intent"
	case "req":
		return "docs/requirements"
	case "arc":
		return "docs/architecture"
	case "adr", "dec":
		return "docs/decisions"
	case "spec":
		return "docs/specifications"
	case "std":
		return "docs/standards"
	case "run":
		return "docs/operations"
	case "rvw":
		return "docs/quality"
	case "scp", "epc", "plan", "trc":
		return "docs/planning"
	case "rel":
		return "docs/records"
	case "fnd":
		return "docs/research"
	case "gls":
		return "docs/vocabulary"
	case "sto":
		return "docs/operating/work-items/stories"
	case "ts":
		return "docs/operating/work-items/technical-stories"
	case "bug":
		return "docs/operating/work-items/bugs"
	case "td":
		return "docs/operating/work-items/tech-debt"
	case "ch":
		return "docs/operating/work-items/chores"
	case "spk":
		return "docs/operating/work-items/spikes"
	case "ctr":
		return "docs/operating/containers"
	case "ses":
		return "docs/operating/sessions"
	case "tkt":
		return "docs/operating/projections"
	}
	return ""
}

// dimensionOfFolder returns the knowledge dimension token of a docs
// folder, or "" when the folder is not a knowledge dimension folder.
func dimensionOfFolder(folder string) string {
	switch folder {
	case "docs/intent":
		return "intent"
	case "docs/requirements":
		return "requirements"
	case "docs/architecture":
		return "architecture"
	case "docs/decisions":
		return "decisions"
	case "docs/specifications":
		return "specifications"
	case "docs/standards":
		return "standards"
	case "docs/operations":
		return "operations"
	case "docs/quality":
		return "quality"
	case "docs/planning":
		return "planning"
	case "docs/records":
		return "records"
	case "docs/research":
		return "research"
	case "docs/vocabulary":
		return "vocabulary"
	}
	return ""
}

// unitFileName renders the repository filename of a unit (validation.md
// Aturan 2): <type-token>-<id>.md/.json, plus the -v<nn> suffix for
// scp-/plan-. The extension follows the content representation:
// structured-json units (v2.0) serialize to the JSON authoring format,
// legacy structured-text units to Markdown.
func unitFileName(u *Unit) string {
	name := u.Identity.Type + "-" + u.Identity.ID
	if conformance.IsVersionedType(u.Identity.Type) {
		name += "-v" + strconv.Itoa(u.Identity.InstanceVersion)
	}
	if u.Content.Representation == StructuredJSON {
		return name + ".json"
	}
	return name + ".md"
}

// unitFilePath renders the repository-relative path of a unit.
func unitFilePath(u *Unit) (string, error) {
	folder := folderForType(u.Identity.Type)
	if folder == "" {
		return "", packageErrorf(
			"import refused: unit %s uses unknown artifact type %q; the repository serialization has no folder for it (this importer implements the 26 EKA type tokens)",
			u.CanonicalIdentityForm, u.Identity.Type)
	}
	// R6 compliance by construction: a knowledge artifact's dimension must
	// equal the dimension of its folder; a mismatch would fail the
	// post-commit validation, so it is refused before any write.
	if info := conformance.TypeInfoFor(u.Identity.Type); info != nil && info.IsKnowledge {
		dim := dimensionOfFolder(folder)
		if u.Classification.Dimension != dim {
			return "", packageErrorf(
				"import refused: unit %s declares dimension %q but its type family serializes to %s (dimension %q); classification would violate rule 6 (dimension == folder)",
				u.CanonicalIdentityForm, u.Classification.Dimension, folder, dim)
		}
	}
	return folder + "/" + unitFileName(u), nil
}

// frontmatter is the deterministic YAML reconstruction of the unit
// metadata. Field order is fixed (design decision 5): identity, revision,
// owned state fields (declared domain order), classification, metadata,
// relationship fields (declared order, only when non-empty), phase, change
// log. yaml.v3 struct marshaling emits fields in declaration order — this
// struct IS the serialization contract.
type frontmatter struct {
	Namespace           string                     `yaml:"namespace"`
	Type                string                     `yaml:"type"`
	ID                  string                     `yaml:"id"`
	InstanceVersion     int                        `yaml:"instance-version"`
	Revision            int                        `yaml:"revision"`
	ContentState        string                     `yaml:"content-state,omitempty"`
	ExecutionState      string                     `yaml:"execution-state,omitempty"`
	PlanningState       string                     `yaml:"planning-state,omitempty"`
	ContainerState      string                     `yaml:"container-state,omitempty"`
	ExistenceState      string                     `yaml:"existence-state,omitempty"`
	Dimension           string                     `yaml:"dimension,omitempty"`
	DimensionsSecondary []string                   `yaml:"dimensions-secondary,omitempty"`
	Author              conformance.AuthorIdentity `yaml:"author,omitempty"`
	Created             string                     `yaml:"created,omitempty"`
	Updated             string                     `yaml:"updated,omitempty"`
	Amends              []string                   `yaml:"amends,omitempty"`
	Supersedes          []string                   `yaml:"supersedes,omitempty"`
	DerivesFrom         []string                   `yaml:"derives-from,omitempty"`
	DependsOn           []string                   `yaml:"depends-on,omitempty"`
	Validates           []string                   `yaml:"validates,omitempty"`
	Phase               string                     `yaml:"phase,omitempty"`
	ChangeLog           []fmChangeLogEntry         `yaml:"change-log,omitempty"`
}

// fmChangeLogEntry is one change-log entry in the fixed YAML field order
// (conformance parseChangeLogEntry reads {date, domain, from, to, by}).
type fmChangeLogEntry struct {
	Date   string                     `yaml:"date"`
	Domain string                     `yaml:"domain"`
	From   string                     `yaml:"from"`
	To     string                     `yaml:"to"`
	By     conformance.AuthorIdentity `yaml:"by"`
}

// buildFrontmatter renders the deterministic YAML metadata block (without
// the surrounding "---" delimiters) of one accepted unit. Relationship
// targets are converted from canonical identity forms to repository
// reference forms (design decision 4).
func buildFrontmatter(u *Unit) (string, error) {
	fm := frontmatter{
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Author:          u.Author,
		Created:         u.Created,
		Updated:         u.Updated,
		Phase:           u.Phase,
	}
	if u.StateVector.ContentState != "" {
		fm.ContentState = u.StateVector.ContentState
	}
	if u.StateVector.ExecutionState != "" {
		fm.ExecutionState = u.StateVector.ExecutionState
	}
	if u.StateVector.PlanningState != "" {
		fm.PlanningState = u.StateVector.PlanningState
	}
	if u.StateVector.ContainerState != "" {
		fm.ContainerState = u.StateVector.ContainerState
	}
	if u.StateVector.ExistenceState != "" {
		fm.ExistenceState = u.StateVector.ExistenceState
	}
	if u.Classification.Dimension != "" {
		fm.Dimension = u.Classification.Dimension
	}
	if len(u.Classification.DimensionsSecondary) > 0 {
		fm.DimensionsSecondary = u.Classification.DimensionsSecondary
	}

	// Relationship fields: nil when absent (yaml omitempty omits nil
	// slices; empty non-nil slices would render as "[]"), present only
	// when the unit carries relationships of that type. Targets are
	// converted to repository reference forms; cross-namespace targets are
	// expressible only at instance-version 1 (v1 limitation, documented).
	rels := map[string][]string{}
	for _, rel := range u.Relationships {
		ref, err := canonicalToRepoReference(rel.Target, u.Identity.Namespace)
		if err != nil {
			return "", err
		}
		rels[rel.Type] = append(rels[rel.Type], ref)
	}
	if refs := rels["amends"]; len(refs) > 0 {
		fm.Amends = refs
	}
	if refs := rels["supersedes"]; len(refs) > 0 {
		fm.Supersedes = refs
	}
	if refs := rels["derives-from"]; len(refs) > 0 {
		fm.DerivesFrom = refs
	}
	if refs := rels["depends-on"]; len(refs) > 0 {
		fm.DependsOn = refs
	}
	if refs := rels["validates"]; len(refs) > 0 {
		fm.Validates = refs
	}

	if len(u.ChangeLog) > 0 {
		fm.ChangeLog = make([]fmChangeLogEntry, 0, len(u.ChangeLog))
		for _, e := range u.ChangeLog {
			fm.ChangeLog = append(fm.ChangeLog, fmChangeLogEntry{
				Date: e.Date, Domain: e.Domain, From: e.From, To: e.To, By: e.By,
			})
		}
	}

	data, err := yaml.Marshal(&fm)
	if err != nil {
		return "", fmt.Errorf("import failed: cannot render frontmatter of %s: %w", u.CanonicalIdentityForm, err)
	}
	return string(data), nil
}

// canonicalToRepoReference converts a canonical identity form target into
// the repository reference grammar (validation.md Aturan 5; design
// decision 4):
//
//	same namespace -> <type>:<id>           (instance-version 1, type not scp-/plan-)
//	                -> <type>:<id>:<n>      (scp-/plan-, always versioned; or n > 1)
//	cross namespace -> <ns>/<type>:<id>     (instance-version 1 only)
//
// The version suffix is the digit form "<type>:<id>:<n>" (the repository
// grammar and every canonical fixture write digits; parseReference reads
// digits — "-v<nn>" is the filename convention, not the reference form).
// A cross-namespace target with instance-version > 1 has no expressible
// reference form in v1: refused with a clear message (documented v1
// limitation).
func canonicalToRepoReference(form, sourceNamespace string) (string, error) {
	id, err := parseCanonicalIdentity(form)
	if err != nil {
		return "", packageErrorf("import refused: relationship target %q is not a canonical identity form: %v", form, err)
	}
	if id.Namespace == sourceNamespace {
		if conformance.IsVersionedType(id.Type) || id.InstanceVersion > 1 {
			return id.Type + ":" + id.ID + ":" + strconv.Itoa(id.InstanceVersion), nil
		}
		return id.Type + ":" + id.ID, nil
	}
	if id.InstanceVersion != 1 {
		return "", packageErrorf(
			"import refused: relationship target %s is a cross-namespace reference to instance v%d; the repository reference grammar expresses cross-namespace references as <ns>/<type>:<id> (line-level, resolving to v1) — instance v%d cannot be expressed (v1 limitation)",
			form, id.InstanceVersion, id.InstanceVersion)
	}
	return id.Namespace + "/" + id.Type + ":" + id.ID, nil
}

// writeOp is one deterministic commit operation: a repository-relative
// file to create with exact content.
type writeOp struct {
	// rel is the repository-relative path with forward slashes.
	rel string
	// data is the exact file content.
	data []byte
	// attachment reports whether the op writes an attachment.
	attachment bool
}

// buildWritePlan renders the deterministic, ordered write plan of the
// accepted content (design decision 7): attachments (sorted by ID) first,
// then accepted units in dependency-resolved commit order. Every op
// targets a file that does not exist yet (a collision — a convention
// document or unknown file already at the target path — aborts the
// import before any write).
func buildWritePlan(pkg *loadedPackage, cl *classification, commitOrder []*Unit, root string) ([]writeOp, error) {
	var ops []writeOp

	for _, a := range cl.newAttachments {
		path := filepath.Join(root, filepath.FromSlash(a.ID))
		if _, err := os.Lstat(path); err == nil {
			return nil, &ConflictError{Conflicts: []ConflictDetail{{
				Identity:    a.ID,
				Differences: []string{"a file already exists at the attachment target path"},
			}}}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("import failed: cannot inspect attachment target %s: %w", a.ID, err)
		}
		ops = append(ops, writeOp{rel: a.ID, data: a.Data, attachment: true})
	}

	for _, u := range commitOrder {
		rel, err := unitFilePath(u)
		if err != nil {
			return nil, err
		}
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Lstat(path); err == nil {
			return nil, &ConflictError{Conflicts: []ConflictDetail{{
				Identity:    u.CanonicalIdentityForm,
				Differences: []string{"a file already exists at the target path " + rel},
			}}}
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("import failed: cannot inspect target path %s: %w", rel, err)
		}
		var data []byte
		if u.Content.Representation == StructuredJSON {
			data, err = buildJSONAuthoring(u)
		} else {
			var fm string
			fm, err = buildFrontmatter(u)
			if err == nil {
				data = append([]byte("---\n"), []byte(fm)...)
				data = append(data, []byte("---\n")...)
				data = append(data, u.ContentPayload...)
			}
		}
		if err != nil {
			return nil, err
		}
		ops = append(ops, writeOp{rel: rel, data: data})
	}
	return ops, nil
}

// jsonAuthoring is the deterministic §3.2 authoring document of one
// structured-json unit (spec-standard-v2 §3.2): the field order is the
// schema's canonical order; unknown top-level fields never occur because
// every field maps to the schema. The content object travels as the
// canonical payload bytes (json.RawMessage), so the written document
// preserves the payload's key order exactly.
type jsonAuthoring struct {
	Namespace           string                     `json:"namespace"`
	Type                string                     `json:"type"`
	ID                  string                     `json:"id"`
	InstanceVersion     int                        `json:"instanceVersion"`
	Revision            int                        `json:"revision"`
	Author              conformance.AuthorIdentity `json:"author,omitempty"`
	Created             string                     `json:"created,omitempty"`
	Updated             string                     `json:"updated,omitempty"`
	State               map[string]string          `json:"state,omitempty"`
	Dimension           string                     `json:"dimension,omitempty"`
	DimensionsSecondary []string                   `json:"dimensionsSecondary,omitempty"`
	Domain              string                     `json:"domain,omitempty"`
	Phase               string                     `json:"phase,omitempty"`
	Relationships       map[string][]string        `json:"relationships,omitempty"`
	ChangeLog           []jsonChangeLogEntry       `json:"changeLog,omitempty"`
	Content             json.RawMessage            `json:"content"`
}

// jsonChangeLogEntry is one change-log entry in the §3.2 spelling:
// domain uses the camelCase state domain names (contentState), values
// unchanged.
type jsonChangeLogEntry struct {
	Date   string                     `json:"date"`
	Domain string                     `json:"domain"`
	From   string                     `json:"from"`
	To     string                     `json:"to"`
	By     conformance.AuthorIdentity `json:"by"`
}

// buildJSONAuthoring renders the JSON-native authoring document of one
// accepted unit: human 2-space indent, trailing newline (spec-standard-v2
// §3.3 authoring encoding). The document re-validates as a repository
// artifact by construction (same folder mapping, same identity fields,
// camelCase domains converted back by the JSON adapter).
func buildJSONAuthoring(u *Unit) ([]byte, error) {
	doc := jsonAuthoring{
		Namespace:       u.Identity.Namespace,
		Type:            u.Identity.Type,
		ID:              u.Identity.ID,
		InstanceVersion: u.Identity.InstanceVersion,
		Revision:        u.Revision,
		Author:          u.Author,
		Created:         u.Created,
		Updated:         u.Updated,
		Dimension:       u.Classification.Dimension,
		Domain:          u.Classification.Domain,
		Phase:           u.Phase,
		Content:         u.ContentPayload,
	}
	if len(u.Classification.DimensionsSecondary) > 0 {
		doc.DimensionsSecondary = u.Classification.DimensionsSecondary
	}
	state := map[string]string{}
	if u.StateVector.ContentState != "" {
		state["contentState"] = u.StateVector.ContentState
	}
	if u.StateVector.ExecutionState != "" {
		state["executionState"] = u.StateVector.ExecutionState
	}
	if u.StateVector.PlanningState != "" {
		state["planningState"] = u.StateVector.PlanningState
	}
	if u.StateVector.ContainerState != "" {
		state["containerState"] = u.StateVector.ContainerState
	}
	if u.StateVector.ExistenceState != "" {
		state["existenceState"] = u.StateVector.ExistenceState
	}
	if len(state) > 0 {
		doc.State = state
	}
	rels := map[string][]string{}
	for _, rel := range u.Relationships {
		ref, err := canonicalToRepoReference(rel.Target, u.Identity.Namespace)
		if err != nil {
			return nil, err
		}
		rels[conformance.StateKeyCamel(rel.Type)] = append(rels[conformance.StateKeyCamel(rel.Type)], ref)
	}
	if len(rels) > 0 {
		doc.Relationships = rels
	}
	if len(u.ChangeLog) > 0 {
		doc.ChangeLog = make([]jsonChangeLogEntry, 0, len(u.ChangeLog))
		for _, e := range u.ChangeLog {
			doc.ChangeLog = append(doc.ChangeLog, jsonChangeLogEntry{
				Date: e.Date, Domain: conformance.StateKeyCamel(e.Domain), From: e.From, To: e.To, By: e.By,
			})
		}
	}
	out, err := json.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("import failed: cannot render authoring document of %s: %w", u.CanonicalIdentityForm, err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, out, "", "  "); err != nil {
		return nil, fmt.Errorf("import failed: cannot render authoring document of %s: %w", u.CanonicalIdentityForm, err)
	}
	indented.WriteByte('\n')
	return indented.Bytes(), nil
}

// commitOrder computes the phase-8 dependency-resolved write order:
// relationship targets before referrers (topological order over the
// package relationship graph). Cycles resolve deterministically by
// canonical identity key (tie-break). All accepted units are written
// regardless (v1 writes only new artifacts; no overwrites), so the order
// is a determinism contract, not a correctness requirement.
func commitOrder(pkg *loadedPackage, cl *classification) []*Unit {
	inPackage := map[string]bool{}
	for _, u := range cl.newUnits {
		inPackage[u.CanonicalIdentityForm] = true
	}

	// Kahn's algorithm over the accepted-unit subgraph.
	indegree := map[string]int{}
	referrers := map[string][]string{} // target form -> referring forms
	for _, u := range cl.newUnits {
		for _, rel := range u.Relationships {
			if inPackage[rel.Target] {
				referrers[rel.Target] = append(referrers[rel.Target], u.CanonicalIdentityForm)
			}
		}
	}
	byForm := map[string]*Unit{}
	for _, u := range cl.newUnits {
		byForm[u.CanonicalIdentityForm] = u
		indegree[u.CanonicalIdentityForm] = 0
	}
	for _, refs := range referrers {
		for _, r := range refs {
			indegree[r]++
		}
	}

	ready := func() []string {
		var forms []string
		for form, deg := range indegree {
			if deg == 0 {
				forms = append(forms, form)
			}
		}
		sort.Strings(forms)
		return forms
	}

	var order []*Unit
	for len(order) < len(cl.newUnits) {
		readyForms := ready()
		if len(readyForms) == 0 {
			// Cycle: emit the lexicographically smallest remaining form and
			// continue (deterministic tie-break).
			var rest []string
			for form, deg := range indegree {
				if deg > 0 {
					rest = append(rest, form)
				}
			}
			sort.Strings(rest)
			if len(rest) == 0 {
				break
			}
			readyForms = []string{rest[0]}
		}
		for _, form := range readyForms {
			order = append(order, byForm[form])
			for _, referrer := range referrers[form] {
				indegree[referrer]--
			}
			delete(indegree, form)
		}
	}
	return order
}

// repoWriter stages files into the repository: every file is written to a
// temp file in the target directory and renamed into place (atomic per
// file). On rollback, every created file and every created directory is
// removed, leaving the repository byte-identical to its pre-import state.
type repoWriter struct {
	root string
	// createdFiles tracks absolute paths of created files (newest last).
	createdFiles []string
	// createdDirs tracks absolute paths of directories created by this
	// run (any order; rollback removes deepest first).
	createdDirs []string
}

// newRepoWriter prepares a writer rooted at the repository.
func newRepoWriter(root string) *repoWriter {
	return &repoWriter{root: root}
}

// write stages one operation: MkdirAll for the parent chain (recording
// only directories this run actually created), temp file + rename.
//
// TOCTOU note (phase 8/9 boundary): the target path is checked at plan
// time (buildWritePlan's Lstat) but the rename happens here, at commit
// time. A path created in between is not re-checked, and os.Rename
// silently overwrites an existing file — a concurrent process writing
// the same path would be overwritten. Mitigations: the CLI is a
// single-user tool operating on its own repository, the commit window
// is short, and the phase-10 revalidation catches repository-level
// fallout. The silent-overwrite window is acknowledged and accepted for
// v1 (documented; no behavior change).
func (w *repoWriter) write(op writeOp) error {
	path := filepath.Join(w.root, filepath.FromSlash(op.rel))
	if err := w.ensureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".eka-import-*.tmp")
	if err != nil {
		return fmt.Errorf("import failed: cannot stage %s: %w", op.rel, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(op.data); err != nil {
		tmp.Close()
		return fmt.Errorf("import failed: cannot stage %s: %w", op.rel, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("import failed: cannot finalize %s: %w", op.rel, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("import failed: cannot commit %s: %w", op.rel, err)
	}
	w.createdFiles = append(w.createdFiles, path)
	return nil
}

// ensureDir creates the directory chain from the repository root down,
// recording only the directories that did not exist before.
func (w *repoWriter) ensureDir(dir string) error {
	rootAbs, err := filepath.Abs(w.root)
	if err != nil {
		return err
	}
	dirAbs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(rootAbs, dirAbs)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	cur := rootAbs
	for _, seg := range strings.Split(rel, string(os.PathSeparator)) {
		cur = filepath.Join(cur, seg)
		if _, err := os.Lstat(cur); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("import failed: cannot inspect %s: %w", cur, err)
		}
		if err := os.Mkdir(cur, 0o755); err != nil {
			return fmt.Errorf("import failed: cannot create directory %s: %w", cur, err)
		}
		w.createdDirs = append(w.createdDirs, cur)
	}
	return nil
}

// rollback removes every file and directory created by this run and
// reports any failure. Created files are removed first; created
// directories are then removed deepest first, then lexicographically
// (deterministic order; os.Remove only removes empty directories, so a
// pre-existing parent chain is never touched). A failure to remove any
// path means the repository may be partially modified — the error names
// the blocked paths (deterministic) and MUST be surfaced by the caller,
// never swallowed.
func (w *repoWriter) rollback() error {
	var failures []string
	for _, f := range w.createdFiles {
		if err := os.Remove(f); err != nil {
			failures = append(failures, f)
		}
	}
	sort.Slice(w.createdDirs, func(i, j int) bool {
		if len(w.createdDirs[i]) != len(w.createdDirs[j]) {
			return len(w.createdDirs[i]) > len(w.createdDirs[j])
		}
		return w.createdDirs[i] < w.createdDirs[j]
	})
	for _, d := range w.createdDirs {
		if err := os.Remove(d); err != nil {
			failures = append(failures, d)
		}
	}
	if len(failures) == 0 {
		return nil
	}
	return fmt.Errorf("rollback could not remove %d created path(s): %s",
		len(failures), strings.Join(failures, ", "))
}

// rollbackOrReport folds a rollback failure into the original error: the
// returned error wraps the original (errors.As still resolves it for the
// CLI's exit-code mapping) and appends a deterministic warning that the
// repository may be partially modified. A failed rollback is never
// reported as a clean rollback.
func rollbackOrReport(orig, rbErr error) error {
	if rbErr == nil {
		return orig
	}
	return fmt.Errorf("%w; WARNING: rollback failed, the repository may be partially modified (%v)",
		orig, rbErr)
}
