package conformance

import (
	"encoding/json"
	"testing"
)

// This file tests the author identity model (RFC: user | agent |
// worker) — the context-sensitive JSON form, the kind validation, and
// the frontmatter parsing of author and change-log by.

func TestAuthorIdentityJSONForms(t *testing.T) {
	// A user serializes as the plain string (legacy byte-identical).
	raw, err := json.Marshal(User("Jonas Berg"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `"Jonas Berg"` {
		t.Errorf("user marshal = %s, want the plain string", raw)
	}
	// An empty identity is an empty user string.
	raw, _ = json.Marshal(AuthorIdentity{})
	if string(raw) != `""` {
		t.Errorf("empty identity marshal = %s, want \"\"", raw)
	}
	// Agent/worker serialize as the structured object.
	raw, _ = json.Marshal(AuthorIdentity{Kind: KindAgent, Name: "agent-x"})
	if string(raw) != `{"kind":"agent","name":"agent-x"}` {
		t.Errorf("agent marshal = %s, want the structured object", raw)
	}
	raw, _ = json.Marshal(AuthorIdentity{Kind: KindWorker, Name: "worker-3"})
	if string(raw) != `{"kind":"worker","name":"worker-3"}` {
		t.Errorf("worker marshal = %s, want the structured object", raw)
	}
	// Round-trip: string and object both decode.
	for _, in := range []string{`"Jonas Berg"`, `{"kind":"agent","name":"agent-x"}`} {
		var a AuthorIdentity
		if err := json.Unmarshal([]byte(in), &a); err != nil {
			t.Fatalf("unmarshal %s: %v", in, err)
		}
		out, _ := json.Marshal(a)
		if string(out) != in {
			t.Errorf("round-trip %s -> %s", in, out)
		}
	}
	// IsUser: empty kind and "user" kind are users.
	if !User("x").IsUser() || !(AuthorIdentity{Kind: KindUser, Name: "x"}).IsUser() {
		t.Error("user identities must report IsUser")
	}
	if (AuthorIdentity{Kind: KindAgent, Name: "x"}).IsUser() {
		t.Error("agent must not report IsUser")
	}
}

func TestAuthorKindValidation(t *testing.T) {
	if !IsAuthorKind(KindUser) || !IsAuthorKind(KindAgent) || !IsAuthorKind(KindWorker) {
		t.Error("the three canonical kinds must validate")
	}
	if IsAuthorKind("bot") {
		t.Error("unknown kind must not validate")
	}
	if len(AuthorKinds) != 3 {
		t.Errorf("AuthorKinds = %v, want the three canonical kinds", AuthorKinds)
	}
}
