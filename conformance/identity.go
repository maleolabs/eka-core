package conformance

import (
	"encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
)

// This file implements the authoring identity model (RFC: author
// identity kinds — user | agent | worker): every authoring identity
// (document `author` and change-log `by`) carries a kind plus a
// display name. The kind "user" is the legacy default: a plain string
// serializes as a user, so existing payloads and machine documents are
// byte-identical. Agent (an AI agent collaborating with a human) and
// worker (an agent's sub-agent) serialize as the structured
// {"kind": ..., "name": ...} form — additive, never ambiguous.

// Identity kinds.
const (
	// KindUser is a human author (the mandatory identity of the
	// ecosystem — the legacy string form).
	KindUser = "user"
	// KindAgent is an AI agent collaborating with a human.
	KindAgent = "agent"
	// KindWorker is a sub-agent of an agent (the agent's worker).
	KindWorker = "worker"
)

// AuthorKinds lists the three canonical identity kinds in declared
// order.
var AuthorKinds = []string{KindUser, KindAgent, KindWorker}

// IsAuthorKind reports whether s is one of the three canonical
// identity kinds.
func IsAuthorKind(s string) bool {
	for _, k := range AuthorKinds {
		if k == s {
			return true
		}
	}
	return false
}

// AuthorIdentity is one authoring identity: the kind plus the display
// name. The zero value ("", "") serializes as an empty user string.
// The JSON form is context-sensitive and backward compatible: a user
// serializes as the plain name string ("Jonas Berg"), an agent or
// worker as the structured object {"kind": "agent", "name": "agent-x"}.
type AuthorIdentity struct {
	Kind string
	Name string
}

// String renders the display name (the identity's name field).
func (a AuthorIdentity) String() string { return a.Name }

// User builds a user identity from a plain name.
func User(name string) AuthorIdentity { return AuthorIdentity{Kind: KindUser, Name: name} }

// IsUser reports whether the identity is a user (the legacy default:
// an empty kind counts as a user).
func (a AuthorIdentity) IsUser() bool { return a.Kind == "" || a.Kind == KindUser }

// MarshalJSON implements the context-sensitive form: a user (or an
// empty identity) serializes as the plain string — byte-identical to
// the legacy author/by fields; agent and worker serialize as the
// structured object.
func (a AuthorIdentity) MarshalJSON() ([]byte, error) {
	if a.IsUser() {
		return json.Marshal(a.Name)
	}
	return json.Marshal(struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	}{Kind: a.Kind, Name: a.Name})
}

// UnmarshalJSON accepts both forms: a plain string (a user) and the
// structured {"kind", "name"} object. Unknown shapes are an error
// (callers surface it as a structural finding).
func (a *AuthorIdentity) UnmarshalJSON(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("empty author identity")
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	return a.decode(v)
}

// MarshalYAML mirrors the JSON form in the authoring frontmatter: a
// user serializes as the plain name, an agent or worker as the
// {kind, name} mapping.
func (a AuthorIdentity) MarshalYAML() (any, error) {
	if a.IsUser() {
		return a.Name, nil
	}
	return map[string]any{"kind": a.Kind, "name": a.Name}, nil
}

// UnmarshalYAML accepts both frontmatter forms (string = user,
// {kind, name} object), mirroring UnmarshalJSON.
func (a *AuthorIdentity) UnmarshalYAML(node *yaml.Node) error {
	var v any
	if err := node.Decode(&v); err != nil {
		return err
	}
	return a.decode(v)
}

// decode parses the generic decoded value: a string (a user) or a
// mapping with "kind" and "name".
func (a *AuthorIdentity) decode(v any) error {
	switch t := v.(type) {
	case string:
		a.Kind = KindUser
		a.Name = t
		return nil
	case map[string]any:
		name, _ := t["name"].(string)
		kind, _ := t["kind"].(string)
		if kind == "" {
			kind = KindUser
		}
		if !IsAuthorKind(kind) || name == "" {
			return fmt.Errorf("author identity object requires kind (%s) and a non-empty name", joinKinds())
		}
		a.Kind = kind
		a.Name = name
		return nil
	default:
		return fmt.Errorf("author identity must be a string or an object {\"kind\", \"name\"}")
	}
}

// joinKinds renders the deterministic "user | agent | worker" list.
func joinKinds() string {
	out := ""
	for i, k := range AuthorKinds {
		if i > 0 {
			out += " | "
		}
		out += k
	}
	return out
}
