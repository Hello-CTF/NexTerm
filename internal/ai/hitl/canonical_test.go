package hitl

import (
	"errors"
	"testing"
)

func TestCanonicalParameters(t *testing.T) {
	first, firstHash, err := CanonicalParameters([]byte(` { "z": [true, null, "\u0061"], "a": {"n": 9007199254740993}} `))
	if err != nil {
		t.Fatal(err)
	}
	second, secondHash, err := CanonicalParameters([]byte(`{"a":{"n":9007199254740993},"z":[true,null,"a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || firstHash != secondHash {
		t.Fatalf("canonical mismatch: %s / %s, %s / %s", first, second, firstHash, secondHash)
	}
	if string(first) != `{"a":{"n":9007199254740993},"z":[true,null,"a"]}` {
		t.Fatalf("canonical JSON = %s", first)
	}
	large, _, err := CanonicalParameters([]byte(`{"number":1e1000}`))
	if err != nil || string(large) != `{"number":1e1000}` {
		t.Fatalf("large number = %s, %v", large, err)
	}
}

func TestCanonicalParametersRejectsAmbiguousJSON(t *testing.T) {
	for _, input := range []string{
		`{"a":1,"a":2}`,
		`{"a":1} {"a":2}`,
		`{"a":}`,
		`[1,2`,
	} {
		if _, _, err := CanonicalParameters([]byte(input)); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("input %q error = %v", input, err)
		}
	}
}

func TestCanonicalParametersDetectsEveryParameterChange(t *testing.T) {
	_, baseline, err := CanonicalParameters([]byte(`{"a":[1,2],"b":1}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{
		`{"a":[2,1],"b":1}`,
		`{"a":[1,2],"b":2}`,
		`{"a":[1,2],"b":1,"c":false}`,
		`{"a":[1,2],"b":1.0}`,
	} {
		_, hash, err := CanonicalParameters([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if hash == baseline {
			t.Errorf("changed parameters %s kept hash %s", input, hash)
		}
	}
}
