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

var ErrVersionMismatch = errors.New("conditional write: version mismatch")

type Expectation struct {
	Exists bool
	Size   int64
	SHA256 string
}

func Absent() Expectation {
	return Expectation{}
}

func Existing(size int64, sha256Hex string) (Expectation, error) {
	expected := Expectation{Exists: true, Size: size, SHA256: strings.ToLower(sha256Hex)}
	if err := expected.Validate(); err != nil {
		return Expectation{}, err
	}
	return expected, nil
}

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

type Version struct {
	Exists bool
	Size   int64
	SHA256 string
}

func VersionOf(data []byte) Version {
	digest := sha256.Sum256(data)
	return Version{Exists: true, Size: int64(len(data)), SHA256: hex.EncodeToString(digest[:])}
}

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

func (v Version) Expectation() Expectation {
	if !v.Exists {
		return Absent()
	}
	return Expectation{Exists: true, Size: v.Size, SHA256: v.SHA256}
}

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

type MismatchError struct {
	Expected Expectation
	Actual   Version
	Reason   string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("conditional write: version mismatch: %s (expected %s, actual %s)", e.Reason, describe(e.Expected.Exists, e.Expected.Size, e.Expected.SHA256), describe(e.Actual.Exists, e.Actual.Size, e.Actual.SHA256))
}

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

var ErrCommitIndeterminate = errors.New("conditional write: commit outcome indeterminate")

type IndeterminateError struct {
	Expected Expectation
	New      Version
	Cause    error
}

func (e *IndeterminateError) Error() string {
	return fmt.Sprintf("conditional write: commit outcome is unknown (%v); re-read the target and reconcile it with the attempted content before any retry", e.Cause)
}

func (e *IndeterminateError) Unwrap() []error {
	if e.Cause == nil {
		return []error{ErrCommitIndeterminate}
	}
	return []error{ErrCommitIndeterminate, e.Cause}
}

type Writer interface {
	WriteFileVersion(ctx context.Context, path string, data []byte, backup bool, expected Expectation) error
}
