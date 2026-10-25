package supervisor

import (
	"encoding/binary"
	"fmt"
	stdsync "sync"

	"github.com/klauspost/compress/zstd"
)

// Live output compression (output_zstd).
//
// Once both peers advertise output_zstd the server may send an output chunk as
// frameOutputZstd instead of frameOutput. Payload layout:
//
//	seq (8 bytes, big-endian) | decoded size (4 bytes, big-endian) | zstd frame
//
// Every frame is an independent zstd frame (no shared context), so reconnects,
// resumes from an offset and replays never depend on earlier frames. The
// sequence number is the recording offset exactly as in frameOutput, so
// ordering, resume skipping and output_credit accounting all work in decoded
// bytes: the credit window bounds unretired decoded output, and compression
// cannot widen it. Chunks below outputZstdMinBytes, or whose compressed form is
// not strictly smaller than the raw frame, are sent as plain frameOutput so
// small interactive echoes pay no latency or size penalty. Replay blocks keep
// using frameReplayGzip when that is negotiated.
const (
	outputZstdMinBytes = 256
	outputZstdHeader   = 8 + 4
	// outputZstdMaxDecoded bounds a single decoded chunk; the server never
	// sends more than replayChunkSize per frame.
	outputZstdMaxDecoded = replayChunkSize
)

var (
	outputZstdEncoders stdsync.Pool
	outputZstdDecoders stdsync.Pool
)

func acquireOutputZstdEncoder() (*zstd.Encoder, error) {
	if pooled, ok := outputZstdEncoders.Get().(*zstd.Encoder); ok {
		return pooled, nil
	}
	// The checksum is skipped: SSH already authenticates the stream and the
	// declared size is verified, so the 4 bytes are better saved.
	return zstd.NewWriter(nil,
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderLevel(zstd.SpeedDefault),
		zstd.WithEncoderCRC(false),
		zstd.WithWindowSize(outputZstdMaxDecoded),
	)
}

func acquireOutputZstdDecoder() (*zstd.Decoder, error) {
	if pooled, ok := outputZstdDecoders.Get().(*zstd.Decoder); ok {
		return pooled, nil
	}
	return zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxWindow(1<<20),
		zstd.WithDecoderMaxMemory(outputZstdMaxDecoded),
	)
}

// encodeOutputZstd returns the frameOutputZstd payload for data, or nil when
// the chunk should be sent raw (too small, or compression does not help).
func encodeOutputZstd(seq uint64, data []byte) ([]byte, error) {
	if len(data) < outputZstdMinBytes || len(data) > outputZstdMaxDecoded {
		return nil, nil
	}
	encoder, err := acquireOutputZstdEncoder()
	if err != nil {
		return nil, err
	}
	defer outputZstdEncoders.Put(encoder)
	header := make([]byte, outputZstdHeader, outputZstdHeader+len(data)/2)
	binary.BigEndian.PutUint64(header, seq)
	binary.BigEndian.PutUint32(header[8:], uint32(len(data)))
	out := encoder.EncodeAll(data, header)
	// Compare against the raw frame (8-byte sequence plus data).
	if len(out) >= 8+len(data) {
		return nil, nil
	}
	return out, nil
}

// parseOutputZstd validates and inflates a frameOutputZstd payload. The
// declared size is capped and the decoded output must match it exactly, so a
// lying header or a zstd bomb is rejected with a protocol error.
func parseOutputZstd(payload []byte) (uint64, []byte, error) {
	if len(payload) <= outputZstdHeader {
		return 0, nil, fmt.Errorf("%w: compressed output frame of %d bytes is too short", ErrProtocol, len(payload))
	}
	seq := binary.BigEndian.Uint64(payload)
	declared := binary.BigEndian.Uint32(payload[8:])
	if declared == 0 || declared > outputZstdMaxDecoded {
		return 0, nil, fmt.Errorf("%w: compressed output declares %d bytes, limit %d", ErrProtocol, declared, outputZstdMaxDecoded)
	}
	decoder, err := acquireOutputZstdDecoder()
	if err != nil {
		return 0, nil, fmt.Errorf("%w: compressed output decoder: %v", ErrProtocol, err)
	}
	defer outputZstdDecoders.Put(decoder)
	data, err := decoder.DecodeAll(payload[outputZstdHeader:], make([]byte, 0, declared))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: invalid compressed output: %v", ErrProtocol, err)
	}
	if len(data) != int(declared) {
		return 0, nil, fmt.Errorf("%w: compressed output decoded to %d bytes, declared %d", ErrProtocol, len(data), declared)
	}
	return seq, data, nil
}
