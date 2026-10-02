package conditional

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExpectationValidation(t *testing.T) {
	valid := VersionOf([]byte("content"))
	if _, err := Existing(valid.Size, valid.SHA256); err != nil {
		t.Fatalf("valid expectation rejected: %v", err)
	}
	if _, err := Existing(valid.Size, strings.ToUpper(valid.SHA256)); err != nil {
		t.Fatalf("constructor should normalise digest case: %v", err)
	}
	if err := Absent().Validate(); err != nil {
		t.Fatalf("absent expectation rejected: %v", err)
	}
	for _, test := range []struct {
		name     string
		expected Expectation
	}{
		{name: "absent with size", expected: Expectation{Size: 1}},
		{name: "absent with digest", expected: Expectation{SHA256: valid.SHA256}},
		{name: "negative size", expected: Expectation{Exists: true, Size: -1, SHA256: valid.SHA256}},
		{name: "short digest", expected: Expectation{Exists: true, Size: 1, SHA256: "abcd"}},
		{name: "non hex digest", expected: Expectation{Exists: true, Size: 1, SHA256: strings.Repeat("zz", 32)}},
		{name: "uppercase digest", expected: Expectation{Exists: true, Size: 1, SHA256: strings.ToUpper(valid.SHA256)}},
	} {
		if err := test.expected.Validate(); err == nil {
			t.Errorf("%s: invalid expectation accepted", test.name)
		}
	}
}

func TestCheckMatchesOnlyExactVersion(t *testing.T) {
	version := VersionOf([]byte("content"))
	expected := version.Expectation()
	if err := expected.Check(version); err != nil {
		t.Fatalf("exact match rejected: %v", err)
	}
	empty := VersionOf(nil)
	if err := empty.Expectation().Check(empty); err != nil {
		t.Fatalf("empty file match rejected: %v", err)
	}
	if err := Absent().Check(Version{}); err != nil {
		t.Fatalf("absent match rejected: %v", err)
	}
	for _, test := range []struct {
		name     string
		expected Expectation
		actual   Version
		reason   string
	}{
		{name: "created file", expected: Absent(), actual: version, reason: "file already exists"},
		{name: "deleted file", expected: expected, actual: Version{}, reason: "file is missing"},
		{name: "size", expected: expected, actual: Version{Exists: true, Size: version.Size + 1, SHA256: version.SHA256}, reason: "size differs"},
		{name: "digest", expected: expected, actual: VersionOf([]byte("contenT")), reason: "content digest differs"},
	} {
		err := test.expected.Check(test.actual)
		var mismatch *MismatchError
		if !errors.As(err, &mismatch) {
			t.Errorf("%s: check = %v, want *MismatchError", test.name, err)
			continue
		}
		if !errors.Is(err, ErrVersionMismatch) {
			t.Errorf("%s: mismatch does not unwrap to ErrVersionMismatch", test.name)
		}
		if mismatch.Reason != test.reason || mismatch.Expected != test.expected || mismatch.Actual != test.actual {
			t.Errorf("%s: mismatch = %+v", test.name, mismatch)
		}
		if !strings.Contains(err.Error(), test.reason) {
			t.Errorf("%s: error text = %q", test.name, err.Error())
		}
	}
}

func TestHashReaderMeasuresAndHonoursCancellation(t *testing.T) {
	data := strings.Repeat("payload", 100000)
	version, err := HashReader(context.Background(), strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if want := VersionOf([]byte(data)); version != want {
		t.Fatalf("version = %+v, want %+v", version, want)
	}
	version, err = HashReader(context.Background(), strings.NewReader(""))
	if err != nil || version != VersionOf(nil) {
		t.Fatalf("empty version = %+v, %v", version, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HashReader(ctx, strings.NewReader(data)); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled hash = %v", err)
	}
	if _, err := HashReader(context.Background(), failingReader{}); err == nil || errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("reader failure = %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic read failure")
}

func TestIndeterminateErrorCarriesReconciliationDetails(t *testing.T) {
	expected := VersionOf([]byte("old")).Expectation()
	attempted := VersionOf([]byte("new"))
	cause := context.Canceled
	err := &IndeterminateError{Expected: expected, New: attempted, Cause: cause}
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("indeterminate error does not match ErrCommitIndeterminate: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("indeterminate error lost its cause: %v", err)
	}
	if errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("indeterminate error must not look like a mismatch: %v", err)
	}
	var typed *IndeterminateError
	if !errors.As(err, &typed) || typed.Expected != expected || typed.New != attempted || typed.Cause != cause {
		t.Fatalf("typed details = %+v", typed)
	}
	if !strings.Contains(err.Error(), "re-read") {
		t.Fatalf("error does not instruct reconciliation: %q", err.Error())
	}
	bare := &IndeterminateError{Expected: expected, New: attempted}
	if !errors.Is(bare, ErrCommitIndeterminate) || bare.Unwrap() == nil {
		t.Fatalf("cause-less indeterminate error = %v", bare)
	}
}
