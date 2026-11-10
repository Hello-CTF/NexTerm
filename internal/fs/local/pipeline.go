package local

import (
	"context"
	"encoding"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// pipelinedSource is a reader that streams a whole file while keeping many
// requests in flight (SFTP WriteTo). A Read loop overlaps only the requests one
// buffer holds, which caps throughput at buffer size per round trip.
type pipelinedSource interface {
	PipelinedWriteTo(io.Writer) (int64, error)
}

// pipelinedTarget is a writer that drains a reader while keeping many writes in
// flight (SFTP ReadFromWithConcurrency). It returns base.ErrUnsupported without
// reading when it cannot pipeline, for example when writes must stay ordered.
type pipelinedTarget interface {
	PipelinedReadFrom(io.Reader) (int64, error)
}

var errTransferBoundReached = errors.New("transfer reached the reported size")

// pipelinedCopy copies offset..total through a pipelined target or source when
// either side offers one. handled is false when neither does and the caller's
// plain loop must run. The copy still stops at total, reports progress through
// advance, and observes ctx between chunks.
func pipelinedCopy(ctx context.Context, source io.Reader, target base.RemoteWriter, offset, total int64, advance func(int64)) (handled bool, transferred int64, err error) {
	if offset == total {
		return false, offset, nil
	}
	if pipelined, ok := target.(pipelinedTarget); ok {
		reader := &boundedReader{ctx: ctx, reader: source, position: offset, limit: total, advance: advance}
		_, err := pipelined.PipelinedReadFrom(reader)
		if errors.Is(err, base.ErrUnsupported) && reader.position == offset {
			return false, offset, nil
		}
		if err == nil && reader.position < total {
			err = io.ErrUnexpectedEOF
		}
		// On error reader.position counts bytes handed to the target, which can exceed the bytes
		// the server acknowledged while writes are in flight. It is progress display only: resume
		// takes the offset from the remote size, never from this value.
		return true, reader.position, err
	}
	if pipelined, ok := source.(pipelinedSource); ok {
		writer := &boundedWriter{ctx: ctx, writer: target, position: offset, limit: total, advance: advance}
		_, err := pipelined.PipelinedWriteTo(writer)
		if errors.Is(err, errTransferBoundReached) {
			err = nil
		}
		if err == nil && writer.position < total {
			err = io.ErrUnexpectedEOF
		}
		return true, writer.position, err
	}
	return false, offset, nil
}

// boundedReader yields source bytes up to limit, checking ctx before each read.
// position is the absolute offset of the next byte. It seeks only when the
// wrapped reader can, which lets a target rewrite a rejected range.
type boundedReader struct {
	ctx      context.Context
	reader   io.Reader
	position int64
	limit    int64
	advance  func(int64)
}

func (r *boundedReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	remaining := r.limit - r.position
	if remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := r.reader.Read(p)
	if n < 0 || n > len(p) {
		return 0, fmt.Errorf("invalid read count %d", n)
	}
	r.position += int64(n)
	if n > 0 {
		r.advance(r.position)
	}
	if errors.Is(err, io.EOF) && r.position < r.limit {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}

func (r *boundedReader) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := r.reader.(io.Seeker)
	if !ok {
		return 0, errors.New("transfer source cannot seek")
	}
	if whence == io.SeekCurrent && offset == 0 {
		return r.position, nil
	}
	if whence != io.SeekStart || offset < 0 || offset > r.limit {
		return 0, fmt.Errorf("unsupported transfer source seek %d/%d", offset, whence)
	}
	position, err := seeker.Seek(offset, io.SeekStart)
	if err != nil {
		return 0, err
	}
	r.position = position
	return position, nil
}

// boundedWriter accepts bytes up to limit and then fails with
// errTransferBoundReached, so a streaming source stops at the reported size.
type boundedWriter struct {
	ctx      context.Context
	writer   io.Writer
	position int64
	limit    int64
	advance  func(int64)
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	room := w.limit - w.position
	if room <= 0 {
		if len(p) == 0 {
			return 0, nil
		}
		return 0, errTransferBoundReached
	}
	chunk := p
	if int64(len(chunk)) > room {
		chunk = chunk[:room]
	}
	written, err := writeAll(w.writer, chunk)
	w.position += written
	if written > 0 {
		w.advance(w.position)
	}
	if err != nil {
		return int(written), err
	}
	if len(chunk) < len(p) {
		return int(written), errTransferBoundReached
	}
	return int(written), nil
}

// hashingSource tees source into digest. When source can stream, the returned
// reader can too, hashing exactly the bytes its destination accepted.
func hashingSource(source io.Reader, digest hash.Hash) io.Reader {
	tee := io.TeeReader(source, digest)
	if pipelined, ok := source.(pipelinedSource); ok {
		return &hashedPipelinedSource{Reader: tee, source: pipelined, digest: digest}
	}
	return tee
}

type hashedPipelinedSource struct {
	io.Reader
	source pipelinedSource
	digest hash.Hash
}

func (h *hashedPipelinedSource) PipelinedWriteTo(w io.Writer) (int64, error) {
	return h.source.PipelinedWriteTo(&hashAfterWrite{writer: w, digest: h.digest})
}

type hashAfterWrite struct {
	writer io.Writer
	digest hash.Hash
}

func (h *hashAfterWrite) Write(p []byte) (int, error) {
	n, err := h.writer.Write(p)
	if n > 0 {
		_, _ = h.digest.Write(p[:n])
	}
	return n, err
}

// rewindableHashingReader tees source into digest like io.TeeReader, but stays seekable: seeking back
// to the position it was created at restores the digest to its state then. A target that gets its
// pipelined writes rejected can then rewind the source and rewrite the range sequentially without
// hashing the bytes of the failed attempt twice. It degrades to a plain tee when the digest cannot
// snapshot its state.
func rewindableHashingReader(source io.ReadSeeker, digest hash.Hash, position int64) io.Reader {
	marshaler, ok := digest.(encoding.BinaryMarshaler)
	unmarshaler, ok2 := digest.(encoding.BinaryUnmarshaler)
	if !ok || !ok2 {
		return io.TeeReader(source, digest)
	}
	state, err := marshaler.MarshalBinary()
	if err != nil {
		return io.TeeReader(source, digest)
	}
	return &rewindableHasher{source: source, digest: digest, restore: unmarshaler, state: state, start: position, position: position}
}

type rewindableHasher struct {
	source   io.ReadSeeker
	digest   hash.Hash
	restore  encoding.BinaryUnmarshaler
	state    []byte
	start    int64
	position int64
}

func (h *rewindableHasher) Read(p []byte) (int, error) {
	n, err := h.source.Read(p)
	if n > 0 {
		_, _ = h.digest.Write(p[:n])
		h.position += int64(n)
	}
	return n, err
}

func (h *rewindableHasher) Seek(offset int64, whence int) (int64, error) {
	if whence == io.SeekCurrent && offset == 0 {
		return h.position, nil
	}
	if whence != io.SeekStart || offset != h.start {
		return 0, fmt.Errorf("unsupported hashed source seek %d/%d", offset, whence)
	}
	if _, err := h.source.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	if err := h.restore.UnmarshalBinary(h.state); err != nil {
		return 0, err
	}
	h.position = h.start
	return h.position, nil
}
