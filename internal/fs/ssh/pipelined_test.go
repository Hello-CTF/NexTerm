package ssh

import (
	"bytes"
	"context"
	"errors"
	"io"
	"math/rand"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
	"github.com/pkg/sftp"
)

// rejectingConcurrentFile stores part of a concurrent burst and then refuses it, like a server
// that does not accept out-of-order writes.
type rejectingConcurrentFile struct {
	fakeWriteFile
	rejectWith error
	bursts     int
}

func (f *rejectingConcurrentFile) ReadFromWithConcurrency(r io.Reader, _ int) (int64, error) {
	f.bursts++
	chunk := make([]byte, sftpPacketSize)
	n, err := io.ReadFull(r, chunk)
	if n > 0 {
		_, _ = f.fakeWriteFile.Write(chunk[:n])
	}
	if f.rejectWith != nil {
		return int64(n), f.rejectWith
	}
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return int64(n), err
	}
	rest, err := io.ReadAll(r)
	_, _ = f.fakeWriteFile.Write(rest)
	return int64(n + len(rest)), err
}

func TestPipelinedReadFromRewritesSequentiallyWhenServerRejectsConcurrentWrites(t *testing.T) {
	payload := make([]byte, 5*sftpPacketSize+17)
	rand.New(rand.NewSource(3)).Read(payload)
	file := &rejectingConcurrentFile{fakeWriteFile: fakeWriteFile{data: map[int64]byte{}}, rejectWith: &sftp.StatusError{Code: 4}}
	writer := &remoteWriter{ctx: context.Background(), file: file}
	n, err := writer.PipelinedReadFrom(bytes.NewReader(payload))
	if err != nil || n != int64(len(payload)) {
		t.Fatalf("PipelinedReadFrom = %d, %v", n, err)
	}
	for i, b := range payload {
		if file.data[int64(i)] != b {
			t.Fatalf("byte %d differs after the sequential rewrite", i)
		}
	}
	if file.bursts != 1 || !writer.sequential || writer.off != int64(len(payload)) {
		t.Fatalf("bursts=%d sequential=%v off=%d", file.bursts, writer.sequential, writer.off)
	}
	for _, size := range file.writes[1:] {
		if size > sftpPacketSize {
			t.Fatalf("sequential rewrite issued a %d-byte request", size)
		}
	}
	if _, err := writer.PipelinedReadFrom(bytes.NewReader(payload)); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("a writer that fell back must not pipeline again: %v", err)
	}
}

func TestPipelinedReadFromKeepsTheErrorWhenTheSourceCannotRewind(t *testing.T) {
	file := &rejectingConcurrentFile{fakeWriteFile: fakeWriteFile{data: map[int64]byte{}}, rejectWith: &sftp.StatusError{Code: 4}}
	writer := &remoteWriter{ctx: context.Background(), file: file}
	source := io.MultiReader(bytes.NewReader(make([]byte, 3*sftpPacketSize)))
	var status *sftp.StatusError
	if _, err := writer.PipelinedReadFrom(source); !errors.As(err, &status) {
		t.Fatalf("unseekable source error = %v, want the server status", err)
	}
}

func TestPipelinedReadFromIsUnsupportedForAppendWriters(t *testing.T) {
	file := &rejectingConcurrentFile{fakeWriteFile: fakeWriteFile{data: map[int64]byte{}}}
	writer := &remoteWriter{ctx: context.Background(), file: file, sequential: true}
	reader := bytes.NewReader([]byte("abc"))
	if _, err := writer.PipelinedReadFrom(reader); !errors.Is(err, base.ErrUnsupported) {
		t.Fatalf("append writer error = %v", err)
	}
	if reader.Len() != 3 || file.bursts != 0 {
		t.Fatal("an unsupported pipeline must not consume the source")
	}
}
