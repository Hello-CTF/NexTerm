package supervisor

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
)

const replayChunkSize = 128 << 10

// Replay blocks share a compression dictionary within each block; live output
// stays uncompressed so interactive writes never wait for a compression flush.
type replayCompressor struct {
	buffer bytes.Buffer
	writer *gzip.Writer
}

func (c *replayCompressor) encode(seq uint64, data []byte) ([]byte, error) {
	c.buffer.Reset()
	c.buffer.Write(outputPayload(seq, nil))
	if c.writer == nil {
		writer, err := gzip.NewWriterLevel(&c.buffer, gzip.BestSpeed)
		if err != nil {
			return nil, err
		}
		c.writer = writer
	} else {
		c.writer.Reset(&c.buffer)
	}
	if _, err := c.writer.Write(data); err != nil {
		return nil, err
	}
	if err := c.writer.Close(); err != nil {
		return nil, err
	}
	return c.buffer.Bytes(), nil
}

func parseReplayGzip(payload []byte) (uint64, []byte, error) {
	seq, compressed, err := parseOutput(payload)
	if err != nil {
		return 0, nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: invalid compressed replay: %v", ErrProtocol, err)
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(io.LimitReader(reader, replayChunkSize+1))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: invalid compressed replay: %v", ErrProtocol, err)
	}
	if len(data) > replayChunkSize {
		return 0, nil, fmt.Errorf("%w: compressed replay exceeds %d bytes", ErrProtocol, replayChunkSize)
	}
	return seq, data, nil
}
