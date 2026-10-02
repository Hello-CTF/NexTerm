// Package conditional defines the shared compare-and-write contract for
// filesystem transports. A conditional write commits only while the target
// still matches the exact version the caller verified: expected existence,
// size and SHA-256 digest. It closes the window between a caller-side
// verification and an unconditional truncate-and-write.
package conditional

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrVersionMismatch matches every error caused by the target no longer
// being the version the caller verified. Use errors.Is with it, or errors.As
// with *MismatchError for the expected and actual versions.
var ErrVersionMismatch = errors.New("conditional write: version mismatch")

// Expectation is the file version a conditional write requires. The zero
// value expects the path to be absent. When Exists is true the target must
// be a regular file of exactly Size bytes with the given lowercase hex
// SHA-256 digest.
type Expectation struct {
	Exists bool
	Size   int64
	SHA256 string
}

// Absent returns an expectation that the path does not exist. A conditional
// create must fail with a mismatch if any file, directory or symlink already
// occupies the path, and must never overwrite it.
func Absent() Expectation {
	return Expectation{}
}

// Existing returns an expectation for an existing regular file version.
func Existing(size int64, sha256Hex string) (Expectation, error) {
	expected := Expectation{Exists: true, Size: size, SHA256: strings.ToLower(sha256Hex)}
	if err := expected.Validate(); err != nil {
		return Expectation{}, err
	}
	return expected, nil
}

// Validate rejects malformed expectations before any filesystem access.
func (e Expectation) Validate() error {
	if !e.Exists {
		if e.Size != 0 || e.SHA256 != "" {
			return errors.New("conditional write: an absent expectation must not carry a size or digest")
		}
		return nil
	}
	if e.Size < 0 {
		return fmt.Errorf("conditional write: expected size must not be negative, got %d", e.Size)
	}
	if len(e.SHA256) != sha256.Size*2 {
		return fmt.Errorf("conditional write: expected SHA-256 must be %d hex characters, got %d", sha256.Size*2, len(e.SHA256))
	}
	if _, err := hex.DecodeString(e.SHA256); err != nil {
		return fmt.Errorf("conditional write: expected SHA-256 is not valid hex: %w", err)
	}
	if e.SHA256 != strings.ToLower(e.SHA256) {
		return errors.New("conditional write: expected SHA-256 must be lowercase hex")
	}
	return nil
}

// Version is an observed file version. The zero value describes an absent
// path; Exists is only set together with the measured size and digest.
type Version struct {
	Exists bool
	Size   int64
	SHA256 string
}

// VersionOf measures the version of an in-memory content.
func VersionOf(data []byte) Version {
	digest := sha256.Sum256(data)
	return Version{Exists: true, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}

// HashReader measures the version of a stream, honouring cancellation
// between chunks.
func HashReader(ctx context.Context, reader io.Reader) (Version, error) {
	digest := sha256.New()
	buffer := make([]byte, 256<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return Version{}, err
		}
		n, readErr := reader.Read(buffer)
		if n > 0 {
			if _, err := digest.Write(buffer[:n]); err != nil {
				return Version{}, err
			}
			size += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return Version{}, readErr
		}
	}
	return Version{Exists: true, Size: size, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

// Expectation converts an observed version into an expectation, for callers
// that snapshot a file while reading it.
func (v Version) Expectation() Expectation {
	if !v.Exists {
		return Absent()
	}
	return Expectation{Exists: true, Size: v.Size, SHA256: v.SHA256}
}

// Check compares the expectation with an observed version. It returns nil
// only for an exact match, and a *MismatchError otherwise.
func (e Expectation) Check(actual Version) error {
	if !e.Exists {
		if actual.Exists {
			return &MismatchError{Expected: e, Actual: actual, Reason: "file already exists"}
		}
		return nil
	}
	if !actual.Exists {
		return &MismatchError{Expected: e, Actual: actual, Reason: "file is missing"}
	}
	if actual.Size != e.Size {
		return &MismatchError{Expected: e, Actual: actual, Reason: "size differs"}
	}
	if actual.SHA256 != e.SHA256 {
		return &MismatchError{Expected: e, Actual: actual, Reason: "content digest differs"}
	}
	return nil
}

// MismatchError reports that the target is not the version the caller
// verified. Reason is a human-readable first difference; Expected and Actual
// carry the compared versions so callers can decide whether to re-read.
type MismatchError struct {
	Expected Expectation
	Actual   Version
	Reason   string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("conditional write: version mismatch: %s (expected %s, actual %s)", e.Reason, describe(e.Expected.Exists, e.Expected.Size, e.Expected.SHA256), describe(e.Actual.Exists, e.Actual.Size, e.Actual.SHA256))
}

// Unwrap makes every mismatch match ErrVersionMismatch through errors.Is.
func (e *MismatchError) Unwrap() error {
	return ErrVersionMismatch
}

func describe(exists bool, size int64, sha256Hex string) string {
	if !exists {
		return "absent file"
	}
	if sha256Hex == "" {
		return "present file"
	}
	return fmt.Sprintf("size %d sha256 %s", size, sha256Hex)
}

// Writer is the conditional-write capability implemented by filesystem
// transports alongside their unconditional base interface.
//
// WriteFileVersion writes data to path only while the target still matches
// expected, preserving the transport's atomic temporary-file replacement and
// backup behaviour. When backup is true and the target holds the verified
// version, that version is copied to the transport's backup path before the
// atomic replacement; a conditional create has nothing to back up.
//
// The write is all-or-nothing: a nil return means the new content was
// committed, and any error return means it was not committed and temporary
// files were removed best effort. In particular a mismatch never truncates,
// replaces or creates the target, and never produces a backup of
// unverified content. On mismatch callers must re-read the target and obtain
// a fresh confirmation instead of retrying with the same expectation.
//
// Paths are passed as filesystem operands only; implementations must not
// interpolate them into shell command lines.
type Writer interface {
	WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected Expectation) error
}
