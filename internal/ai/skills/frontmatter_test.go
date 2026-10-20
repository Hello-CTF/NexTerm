package skills_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/ai/skills"
)

const (
	testMaxNameBytes        = 64
	testMaxDescriptionBytes = 256
	testMaxBodyBytes        = 64 * 1024
)

func TestParseValidDocument(t *testing.T) {
	document := "---\nname: demo-skill\ndescription: A synthetic demo skill\n---\n\n# Demo\n\nDo the demo steps.\n"
	skill, err := skills.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Name != "demo-skill" || skill.Description != "A synthetic demo skill" {
		t.Fatalf("skill = %+v", skill)
	}
	if skill.Body != "# Demo\n\nDo the demo steps.\n" {
		t.Fatalf("body = %q", skill.Body)
	}
}

func TestParseNormalizesCRLFAndTrimsLeadingBlankLines(t *testing.T) {
	document := "---\r\nname: demo-skill\r\ndescription: demo\r\n---\r\n\r\n\r\nbody line\r\n"
	skill, err := skills.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Body != "body line\n" {
		t.Fatalf("body = %q", skill.Body)
	}
	again, err := skills.Parse([]byte(strings.ReplaceAll(document, "\r\n", "\n")))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Digest() != again.Digest() {
		t.Fatal("CRLF and LF documents produced different digests")
	}
}

func TestParseKeepsBodyVerbatim(t *testing.T) {
	document := "---\nname: demo-skill\ndescription: demo\n---\n---\nnot a delimiter anymore\n  indented\n"
	skill, err := skills.Parse([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if skill.Body != "---\nnot a delimiter anymore\n  indented\n" {
		t.Fatalf("body = %q", skill.Body)
	}
}

func TestParseRejectsInvalidDocuments(t *testing.T) {
	cases := map[string]string{
		"missing opening delimiter": "name: demo\ndescription: demo\n---\nbody\n",
		"unterminated front matter": "---\nname: demo\ndescription: demo\n",
		"unknown key":               "---\nname: demo\ndescription: demo\nextra: x\n---\nbody\n",
		"duplicate key":             "---\nname: demo\nname: demo\ndescription: demo\n---\nbody\n",
		"empty value":               "---\nname: demo\ndescription:\n---\nbody\n",
		"missing name":              "---\ndescription: demo\n---\nbody\n",
		"missing description":       "---\nname: demo\n---\nbody\n",
		"invalid line":              "---\nname demo\n---\nbody\n",
		"uppercase name":            "---\nname: Demo\ndescription: demo\n---\nbody\n",
		"underscore name":           "---\nname: demo_skill\ndescription: demo\n---\nbody\n",
		"leading dash name":         "---\nname: -demo\ndescription: demo\n---\nbody\n",
		"empty body":                "---\nname: demo\ndescription: demo\n---\n\n\n",
		"oversized name":            "---\nname: " + strings.Repeat("a", testMaxNameBytes+1) + "\ndescription: demo\n---\nbody\n",
		"oversized description":     "---\nname: demo\ndescription: " + strings.Repeat("d", testMaxDescriptionBytes+1) + "\n---\nbody\n",
		"oversized body":            "---\nname: demo\ndescription: demo\n---\n" + strings.Repeat("b", testMaxBodyBytes+1) + "\n",
	}
	for label, document := range cases {
		if _, err := skills.Parse([]byte(document)); !errors.Is(err, skills.ErrInvalidSkill) {
			t.Errorf("%s: err = %v, want ErrInvalidSkill", label, err)
		}
	}
}
