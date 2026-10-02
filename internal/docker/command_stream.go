package docker

import (
	"errors"
	"io"
	"sync"
)

func combinedCommandOutput(stream CommandStream) io.ReadCloser {
	provider, ok := stream.(interface{ Stderr() io.Reader })
	if !ok || provider.Stderr() == nil {
		return stream
	}
	reader, writer := io.Pipe()
	result := &mergedCommandReader{PipeReader: reader, stream: stream}
	go func() {
		var copies sync.WaitGroup
		copies.Add(2)
		copyOutput := func(source io.Reader) {
			defer copies.Done()
			_, err := io.Copy(writer, source)
			if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				_ = stream.Close()
			}
		}
		go copyOutput(stream)
		go copyOutput(provider.Stderr())
		copies.Wait()
		_ = writer.Close()
	}()
	return result
}

type mergedCommandReader struct {
	*io.PipeReader
	stream    CommandStream
	closeOnce sync.Once
	closeErr  error
}

func (r *mergedCommandReader) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.stream.Close()
		if err := r.PipeReader.Close(); r.closeErr == nil {
			r.closeErr = err
		}
	})
	return r.closeErr
}
