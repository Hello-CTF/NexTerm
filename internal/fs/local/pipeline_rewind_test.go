package local

import (
	"bytes"
	"crypto/sha256"
	"io"
	"testing"
)

// A resume upload hashes the bytes it sends. When a pipelined target rejects the burst and rewinds
// the source, the digest must forget the failed attempt instead of counting those bytes twice.
func TestRewindableHashingReaderRestoresDigestOnRewind(t *testing.T) {
	data := randomPayload(50_000, 11)
	const offset = 10_000
	prefix := data[:offset]
	digest := sha256.New()
	digest.Write(prefix)
	source := bytes.NewReader(data)
	if _, err := source.Seek(offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader := rewindableHashingReader(source, digest, offset)
	seeker, ok := reader.(io.Seeker)
	if !ok {
		t.Fatal("hashing reader is not seekable")
	}
	if _, err := io.ReadFull(reader, make([]byte, 7000)); err != nil {
		t.Fatal(err)
	}
	if position, err := seeker.Seek(0, io.SeekCurrent); err != nil || position != offset+7000 {
		t.Fatalf("position = %d, %v", position, err)
	}
	if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(rest, data[offset:]) {
		t.Fatalf("read after rewind = %d bytes, %v", len(rest), err)
	}
	want := sha256.Sum256(data)
	if got := digest.Sum(nil); !bytes.Equal(got, want[:]) {
		t.Fatal("digest counted the rewound bytes twice")
	}
	if _, err := seeker.Seek(offset+1, io.SeekStart); err == nil {
		t.Fatal("seek to an arbitrary position must be refused")
	}
}
