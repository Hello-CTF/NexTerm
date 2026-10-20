package skills_test

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
)

func TestDigestPinsExactDispatchedMaterial(t *testing.T) {
	skill := skills.Skill{Name: "demo-skill", Description: "demo", Body: "body line\n"}
	sum := sha256.Sum256([]byte("demo-skill\x00demo\x00body line\n"))
	expected := "sha256:" + hex.EncodeToString(sum[:])
	if skill.Digest() != expected || skills.DigestOf(skill) != expected {
		t.Fatalf("digest = %q, want %q", skill.Digest(), expected)
	}
	other := skill
	other.Body = "body line\n\n"
	if other.Digest() == skill.Digest() {
		t.Fatal("trailing newline change did not change the digest")
	}
}

// TestOverrideCarriesOnlyInstructionFields pins the security boundary that
// user overrides affect model instructions only: the persisted override
// shape must never grow fields that could carry guard patterns or
// screen.json content.
func TestOverrideCarriesOnlyInstructionFields(t *testing.T) {
	allowed := map[string]bool{"Name": true, "Description": true, "Body": true, "Disabled": true, "UpdatedAt": true}
	typeOf := reflect.TypeOf(skills.Override{})
	for index := 0; index < typeOf.NumField(); index++ {
		if !allowed[typeOf.Field(index).Name] {
			t.Fatalf("Override grew unexpected field %q", typeOf.Field(index).Name)
		}
	}
	input := reflect.TypeOf(skills.OverrideInput{})
	for index := 0; index < input.NumField(); index++ {
		if !allowed[input.Field(index).Name] {
			t.Fatalf("OverrideInput grew unexpected field %q", input.Field(index).Name)
		}
	}
}
