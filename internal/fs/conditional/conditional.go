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

// ErrCommitIndeterminate matches errors returned when the commit may or may
// not have taken effect, for example when a remote transport executes the
// atomic commit but the response is lost. Use errors.As with
// *IndeterminateError for reconciliation details.
var ErrCommitIndeterminate = errors.New("conditional write: commit outcome indeterminate")

// IndeterminateError reports that the outcome of the commit is unknown. It
// is the only error under which the new content may already be live; callers
// must re-read the target and reconcile it against Expected and New before
// doing anything else. Implementations never retry the operation
// automatically, because the external effect is ambiguous.
type IndeterminateError struct {
	Expected Expectation
	New      Version
	Cause    error
}

func (e *IndeterminateError) Error() string {
	return fmt.Sprintf("conditional write: commit outcome is unknown (%v); re-read the target and reconcile it with the attempted content before any retry", e.Cause)
}

// Unwrap matches ErrCommitIndeterminate and, when present, the underlying
// transport cause (for example context.Canceled) through errors.Is.
func (e *IndeterminateError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrCommitIndeterminate}
	}
	return []error{ErrCommitIndeterminate, e.Cause}
}

// Writer is the conditional-write capability implemented by filesystem
// transports alongside their unconditional base interface.
//
// WriteFileVersion writes data to path only while the target still matches
// expected, preserving the transport's atomic temporary-file replacement and
// backup behaviour. When backup is true and the target holds the verified
// version, those exact verified bytes are copied to the transport's backup
// path before the atomic replacement; a conditional create has nothing to
// back up and never writes a backup.
//
// Support is deliberately limited to what each platform can actually enforce:
//   - A conditional create (expected absent) is supported by every
//     transport. The commit is an atomic no-clobber primitive (a hard link
//     or a move that fails if the path appeared), so a conflicting create is
//     rejected as a mismatch even if it lands after the last check.
//   - An existing-file replacement is supported only where other processes'
//     content writes are kernel-enforced to stay out for the whole
//     verify-and-commit operation: local Windows (share modes plus a
//     mandatory byte-range lock held across the commit) and WinRM (the same
//     mechanism inside a single remote script, with the locked handle still
//     open during File.Replace). The path's identity/content is re-checked
//     immediately before the commit. Unix local filesystems and the SFTP
//     protocol offer no such exclusion or compare-and-commit primitive, so
//     their replacements fail with an error wrapping base.ErrUnsupported
//     before any mutation instead of pretending to be safe.
//
// The outcome of every call is exactly one of:
//   - nil: the new content was committed under the guarantees above.
//   - *MismatchError (errors.Is ErrVersionMismatch): the target was not the
//     verified version; nothing was committed and no backup was written.
//     Callers must re-read and obtain a fresh confirmation.
//   - *IndeterminateError (errors.Is ErrCommitIndeterminate): the commit may
//     or may not have happened; callers must re-read and reconcile, and no
//     automatic retry has been attempted.
//   - an error wrapping base.ErrUnsupported: the transport cannot enforce
//     the contract for this operation; no mutation was attempted.
//   - any other error: the new content was not committed and temporary
//     files were removed best effort.
//
// Paths are passed as filesystem operands only; implementations must not
// interpolate them into shell command lines.
type Writer interface {
	WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected Expectation) error
}
