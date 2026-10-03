package docker

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/ProbiusOfficial/NexTerm/internal/transport/base"
)

func combinedCommandOutput(stream CommandStream) io.ReadCloser {
	ctx, cancel := context.WithCancel(context.Background())
	var ordered base.OrderedOutput
	if output, ok := stream.(base.OrderedOutput); ok {
		ordered = output
	} else {
		provider, ok := stream.(interface{ Stderr() io.Reader })
		if !ok || provider.Stderr() == nil {
			cancel()
			return stream
		}
		ordered = newCommandOutputMultiplexer(ctx, stream, provider.Stderr())
	}
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
			if event.Err != nil {
				_ = writer.CloseWithError(event.Err)
				return
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
