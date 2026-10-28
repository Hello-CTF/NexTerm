package chunked_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/fs/chunked"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// r3 P2: a malicious or corrupt staging params file with an out-of-range
// declared size must fail closed before any large allocation or read. Only
// 19..65554-byte version 2 params are accepted; the 16-byte params of older
// builds are rejected too.
type declaredSizeFS struct {
	base.FileSystem
	size int64
	read bool
}

func (f *declaredSizeFS) List(_ context.Context, dir string) ([]base.FileEntry, error) {
	return []base.FileEntry{{
		Name: ".remote.bin.nexterm-00000000000000000000000001.params",
		Path: filepath.Join(dir, ".remote.bin.nexterm-00000000000000000000000001.params"),
		Kind: base.FileFile, Size: f.size,
	}}, nil
}

func (f *declaredSizeFS) OpenRead(context.Context, string) (base.RemoteReader, error) {
	return &declaredSizeReader{size: f.size, fs: f}, nil
}

type declaredSizeReader struct {
	size int64
	fs   *declaredSizeFS
}

func (r *declaredSizeReader) Read([]byte) (int, error) {
	r.fs.read = true
	return 0, errors.New("params content must not be read for an invalid declared size")
}

func (r *declaredSizeReader) Close() error { return nil }
func (r *declaredSizeReader) Size() int64  { return r.size }

func TestScanStagingRejectsOutOfRangeParamsSizeWithoutReading(t *testing.T) {
	for _, size := range []int64{0, 16, 17, 18, 65555, 1 << 40} {
		destination := &declaredSizeFS{size: size}
		staged, err := chunked.ScanStaging(context.Background(), destination, "/dir/remote.bin")
		if err != nil {
			t.Fatalf("ScanStaging size %d: %v", size, err)
		}
		if len(staged) != 1 || staged[0].HasParams {
			t.Fatalf("size %d staged = %+v, want params-less group", size, staged)
		}
		if destination.read {
			t.Fatalf("size %d: params content was read despite the invalid declared size", size)
		}
	}
}

func TestNewRejectsOversizedOwner(t *testing.T) {
	if _, err := chunked.New(testUploadID, "remote.bin", 4, 4, chunked.WithOwner(strings.Repeat("o", 65536))); err == nil {
		t.Fatal("oversized owner accepted")
	}
	if _, err := chunked.New(testUploadID, "remote.bin", 4, 4, chunked.WithOwner(strings.Repeat("o", 65535))); err != nil {
		t.Fatalf("max-size owner rejected: %v", err)
	}
}
