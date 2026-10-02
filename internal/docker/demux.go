package docker

import (
	"io"
	"sync"

	"github.com/moby/moby/api/pkg/stdcopy"
)

func demultiplexReadCloser(source io.ReadCloser, tty bool) io.ReadCloser {
	if tty {
		return source
	}
	reader, writer := io.Pipe()
	result := &demuxReadCloser{PipeReader: reader, source: source}
	go func() {
		_, err := stdcopy.StdCopy(writer, writer, source)
		_ = source.Close()
		_ = writer.CloseWithError(err)
	}()
	return result
}

type demuxReadCloser struct {
	*io.PipeReader
	source    io.ReadCloser
	closeOnce sync.Once
	closeErr  error
}

func (r *demuxReadCloser) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.source.Close()
		if err := r.PipeReader.Close(); r.closeErr == nil {
			r.closeErr = err
		}
	})
	return r.closeErr
}
