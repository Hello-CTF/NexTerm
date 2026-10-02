package docker

import (
	"context"
	"errors"
	"io"
	"sync"
)

type CommandOutputEvent struct {
	Data   []byte
	Stderr bool
}

type OrderedCommandOutput interface {
	NextOutput(ctx context.Context) (CommandOutputEvent, error)
}

func combinedCommandOutput(stream CommandStream) io.ReadCloser {
	if ordered, ok := stream.(OrderedCommandOutput); ok {
		ctx, cancel := context.WithCancel(context.Background())
		reader, writer := io.Pipe()
		result := &mergedCommandReader{PipeReader: reader, stream: stream, cancel: cancel}
		go func() {
			defer cancel()
			for {
				event, err := ordered.NextOutput(ctx)
				if len(event.Data) > 0 {
					if _, writeErr := writer.Write(event.Data); writeErr != nil {
						_ = stream.Close()
						return
					}
				}
				if err != nil {
					if errors.Is(err, io.EOF) {
						_ = writer.Close()
					} else {
						_ = writer.CloseWithError(err)
					}
					return
				}
			}
		}()
		return result
	}
	provider, ok := stream.(interface{ Stderr() io.Reader })
	if !ok || provider.Stderr() == nil {
		return stream
	}
	return &sequentialCommandReader{stdout: stream, stderr: provider.Stderr(), stream: stream}
}

type mergedCommandReader struct {
	*io.PipeReader
	stream    CommandStream
	cancel    context.CancelFunc
	closeOnce sync.Once
	closeErr  error
}

func (r *mergedCommandReader) Close() error {
	r.closeOnce.Do(func() {
		r.cancel()
		r.closeErr = r.stream.Close()
		if err := r.PipeReader.Close(); r.closeErr == nil {
			r.closeErr = err
		}
	})
	return r.closeErr
}

type sequentialCommandReader struct {
	stdout    io.Reader
	stderr    io.Reader
	stream    CommandStream
	closeOnce sync.Once
	closeErr  error
}

func (r *sequentialCommandReader) Read(buffer []byte) (int, error) {
	if r.stdout != nil {
		count, err := r.stdout.Read(buffer)
		if errors.Is(err, io.EOF) {
			r.stdout = nil
			if count > 0 {
				return count, nil
			}
		} else {
			return count, err
		}
	}
	return r.stderr.Read(buffer)
}

func (r *sequentialCommandReader) Close() error {
	r.closeOnce.Do(func() {
		r.closeErr = r.stream.Close()
	})
	return r.closeErr
}
