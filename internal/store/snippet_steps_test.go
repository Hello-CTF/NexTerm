package store

import (
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

func TestValidateSnippetStepsWaitCondition(t *testing.T) {
	step := func(mutate func(*SnippetStep)) []SnippetStep {
		s := SnippetStep{ID: "a", Command: "echo"}
		mutate(&s)
		return []SnippetStep{s}
	}
	valid := map[string][]SnippetStep{
		"no wait":                        step(func(*SnippetStep) {}),
		"plain text":                     step(func(s *SnippetStep) { s.WaitFor = "$ " }),
		"regex":                          step(func(s *SnippetStep) { s.WaitFor = `[$#] $`; s.WaitForRegex = true }),
		"timeout lower end":              step(func(s *SnippetStep) { s.WaitFor = "ok"; s.TimeoutMs = SnippetMinWaitTimeoutMs }),
		"timeout upper end":              step(func(s *SnippetStep) { s.WaitFor = "ok"; s.TimeoutMs = SnippetMaxWaitTimeoutMs }),
		"plain text that is not a regex": step(func(s *SnippetStep) { s.WaitFor = "(" }),
	}
	for name, steps := range valid {
		if err := ValidateSnippetSteps(steps); err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
	invalid := map[string][]SnippetStep{
		"regex flag without pattern":  step(func(s *SnippetStep) { s.WaitForRegex = true }),
		"timeout without pattern":     step(func(s *SnippetStep) { s.TimeoutMs = 5000 }),
		"blank pattern":               step(func(s *SnippetStep) { s.WaitFor = "   " }),
		"oversized pattern":           step(func(s *SnippetStep) { s.WaitFor = strings.Repeat("x", SnippetMaxWaitForBytes+1) }),
		"timeout too short":           step(func(s *SnippetStep) { s.WaitFor = "ok"; s.TimeoutMs = SnippetMinWaitTimeoutMs - 1 }),
		"timeout too long":            step(func(s *SnippetStep) { s.WaitFor = "ok"; s.TimeoutMs = SnippetMaxWaitTimeoutMs + 1 }),
		"negative timeout":            step(func(s *SnippetStep) { s.WaitFor = "ok"; s.TimeoutMs = -1 }),
		"regex that does not compile": step(func(s *SnippetStep) { s.WaitFor = "("; s.WaitForRegex = true }),
		"regex with a back reference": step(func(s *SnippetStep) { s.WaitFor = `(a)\1`; s.WaitForRegex = true }),
	}
	for name, steps := range invalid {
		err := ValidateSnippetSteps(steps)
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		requireCode(t, err, ipc.CodeBadParam)
	}
}

func TestNormalizeSnippetContentKeepsWaitCondition(t *testing.T) {
	steps := []SnippetStep{
		{ID: "a", Command: "ssh bastion"},
		{ID: "b", Command: "sudo -i", WaitFor: `\$ $`, WaitForRegex: true, TimeoutMs: 30000},
	}
	_, _, encoded, err := NormalizeSnippetContent(SnippetContent{Steps: steps})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSnippetSteps(encoded)
	if err != nil || len(decoded) != 2 || decoded[0] != steps[0] || decoded[1] != steps[1] {
		t.Fatalf("decoded = %+v, %v", decoded, err)
	}
	// A step without a wait encodes exactly as before, so existing snippets keep their stored and synced bytes.
	plain, _, plainEncoded, err := NormalizeSnippetContent(SnippetContent{Steps: steps[:1]})
	if err != nil || plain != "" || plainEncoded != `[{"id":"a","name":"","command":"ssh bastion"}]` {
		t.Fatalf("plain step encoded as %s, %v", plainEncoded, err)
	}
}
