package ipc

import "testing"

func TestUserRevocationsStaleRules(t *testing.T) {
	var r UserRevocations
	before := r.Epoch("u1")
	r.Revoke("u1", "current")
	if !r.Stale("u1", before, "other") {
		t.Fatal("an operation started before the revocation by another session must be stale")
	}
	if r.Stale("u1", before, "current") {
		t.Fatal("the spared session must not be stale")
	}
	if r.Stale("u2", before, "other") || r.Stale("", before, "") {
		t.Fatal("other users are unaffected")
	}
	after := r.Epoch("u1")
	if r.Stale("u1", after, "other") {
		t.Fatal("an operation started after the revocation is not stale")
	}
	r.Revoke("u1", "")
	if !r.Stale("u1", after, "") || !r.Stale("u1", after, "current") {
		t.Fatal("a revocation sparing nothing stales everyone, including an empty session")
	}
}

func TestMatchesRevoked(t *testing.T) {
	if !MatchesRevoked("", "") || !MatchesRevoked("a", "") || MatchesRevoked("a", "a") || !MatchesRevoked("", "a") {
		t.Fatal("MatchesRevoked rules wrong")
	}
}
