package sync

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"io"
	stdsync "sync"

	"github.com/klauspost/compress/zstd"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
)

// Compressed transcript objects.
//
// A compressed transcript is sealed under its own AAD label (wireKindTranscriptZstd) instead of KindTranscript.
// Peers that predate this format try only the old kind labels, fail authentication and quarantine the object with a
// visible "decrypt failed" warning; they can never apply it as an empty, content-less transcript. Readers in this
// version map the label back to KindTranscript and hand the logical JSON to the unchanged apply path, so payload
// hashes and LWW decisions never depend on how an object was encoded on the wire.
//
// Two labels exist. wireKindTranscriptGzip is what the writer emits: the browser client reads it with the platform
// DecompressionStream("gzip") at no bundle cost (measured about 5 percent larger than zstd on terminal output, at
// similar speed). wireKindTranscriptZstd stays readable for objects written by builds that emitted it; the browser
// and syncv2-helper do not read it and fail visibly.
//
// Sealed plaintext layout: "NXZ1" | uint64 big-endian decoded size | gzip stream (or zstd frame for the zstd label).
const (
	wireKindTranscriptGzip = "transcript.gzip"
	wireKindTranscriptZstd = "transcript.zstd"

	compressedMagic      = "NXZ1"
	compressedHeaderSize = len(compressedMagic) + 8
	// compressMinBytes: below this the zstd frame overhead is not worth a format change.
	compressMinBytes = 1 << 10
	// compressedMaxDecoded is the absolute decode cap, aligned with the largest accepted plaintext.
	compressedMaxDecoded = maxApplyObjectBytes
)

// DefaultTranscriptCompression is the writer default. Readers always understand the compressed form; enabling the
// writer is a second rollout phase because devices that predate the reader cannot open these objects.
//
// Rollout: (1) ship the readers (Go engine, browser crypto.ts, syncv2-helper) and let them reach every device and
// browser of the account; (2) only then switch the writer on per account via Engine.SetTranscriptCompression. The
// device registry (user_device) carries no per-device capability today and browser sessions are not devices, so the
// engine cannot prove "every peer can read" automatically; the switch is therefore a deliberate manual step. A device
// that cannot read the label quarantines the object with a visible decrypt failure, never an empty transcript.
const DefaultTranscriptCompression = false

var (
	errCompressedFormat = errors.New("invalid compressed sync object")

	gzipWriters stdsync.Pool

	zstdEncoders stdsync.Pool
	zstdDecoders stdsync.Pool
)

func acquireZstdEncoder() (*zstd.Encoder, error) {
	if pooled, ok := zstdEncoders.Get().(*zstd.Encoder); ok {
		return pooled, nil
	}
	return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedDefault))
}

func acquireZstdDecoder() (*zstd.Decoder, error) {
	if pooled, ok := zstdDecoders.Get().(*zstd.Decoder); ok {
		return pooled, nil
	}
	// Output is already capped by the declared size; bounding the window and memory is defence in depth.
	return zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(64<<20), zstd.WithDecoderMaxMemory(uint64(compressedMaxDecoded)))
}

// compressObjectPlaintext returns the gzip layout, or nil when compression is not worthwhile.
func compressObjectPlaintext(plaintext []byte) ([]byte, error) {
	if len(plaintext) < compressMinBytes || len(plaintext) > compressedMaxDecoded {
		return nil, nil
	}
	out := bytes.NewBuffer(make([]byte, 0, compressedHeaderSize+len(plaintext)/4))
	out.WriteString(compressedMagic)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(plaintext)))
	out.Write(size[:])
	writer, ok := gzipWriters.Get().(*gzip.Writer)
	if ok {
		writer.Reset(out)
	} else {
		var err error
		if writer, err = gzip.NewWriterLevel(out, gzip.DefaultCompression); err != nil {
			return nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步对象", err)
		}
	}
	defer gzipWriters.Put(writer)
	if _, err := writer.Write(plaintext); err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步对象", err)
	}
	if err := writer.Close(); err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步对象", err)
	}
	if out.Len() >= len(plaintext) {
		return nil, nil
	}
	return out.Bytes(), nil
}

// compressObjectPlaintextZstd produces the zstd layout. The writer no longer uses it; it exists for interop tests.
func compressObjectPlaintextZstd(plaintext []byte) ([]byte, error) {
	encoder, err := acquireZstdEncoder()
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步对象", err)
	}
	defer zstdEncoders.Put(encoder)
	header := make([]byte, compressedHeaderSize, compressedHeaderSize+len(plaintext)/2)
	copy(header, compressedMagic)
	binary.BigEndian.PutUint64(header[len(compressedMagic):], uint64(len(plaintext)))
	return encoder.EncodeAll(plaintext, header), nil
}

// decompressGzipPlaintext inflates the gzip layout with the same bounds as the zstd path: declared size 1..cap,
// exact match, and at most one extra byte read.
func decompressGzipPlaintext(data []byte) ([]byte, error) {
	declared, err := compressedDeclaredSize(data)
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(data[compressedHeaderSize:]))
	if err != nil {
		return nil, errCompressedFormat
	}
	defer reader.Close()
	buffer := bytes.NewBuffer(make([]byte, 0, min(declared, uint64(4<<20))))
	n, err := io.Copy(buffer, io.LimitReader(reader, int64(declared)+1))
	if err != nil || uint64(n) != declared {
		return nil, errCompressedFormat
	}
	return buffer.Bytes(), nil
}

func compressedDeclaredSize(data []byte) (uint64, error) {
	if len(data) <= compressedHeaderSize || string(data[:len(compressedMagic)]) != compressedMagic {
		return 0, errCompressedFormat
	}
	declared := binary.BigEndian.Uint64(data[len(compressedMagic):compressedHeaderSize])
	if declared == 0 || declared > uint64(compressedMaxDecoded) {
		return 0, errCompressedFormat
	}
	return declared, nil
}

// decompressObjectPlaintext inflates the zstd layout. The declared size is bounded by the absolute cap and the
// output must match it exactly, so a lying header cannot cause an expansion beyond what was declared.
func decompressObjectPlaintext(data []byte) ([]byte, error) {
	declared, err := compressedDeclaredSize(data)
	if err != nil {
		return nil, err
	}
	decoder, err := acquireZstdDecoder()
	if err != nil {
		return nil, err
	}
	if err := decoder.Reset(bytes.NewReader(data[compressedHeaderSize:])); err != nil {
		zstdDecoders.Put(decoder)
		return nil, errCompressedFormat
	}
	defer func() {
		_ = decoder.Reset(nil)
		zstdDecoders.Put(decoder)
	}()
	out := make([]byte, 0, min(declared, uint64(4<<20)))
	buffer := bytes.NewBuffer(out)
	// Reading one byte past the declared size detects both lying headers and bombs without unbounded allocation.
	n, err := io.Copy(buffer, io.LimitReader(decoder, int64(declared)+1))
	if err != nil || uint64(n) != declared {
		return nil, errCompressedFormat
	}
	return buffer.Bytes(), nil
}

// sealObjectWire seals an object, compressing transcript content when allowed and worthwhile.
func sealObjectWire(dek, plaintext []byte, id, kind string, compress bool) ([]byte, error) {
	if compress && kind == KindTranscript {
		packed, err := compressObjectPlaintext(plaintext)
		if err != nil {
			return nil, err
		}
		if packed != nil {
			return sealObject(dek, packed, id, wireKindTranscriptGzip)
		}
	}
	return sealObject(dek, plaintext, id, kind)
}

// openCompressedTranscript tries the compressed labels and returns the logical transcript JSON.
func openCompressedTranscript(dek, blob []byte, id string) ([]byte, bool, error) {
	for _, label := range []string{wireKindTranscriptGzip, wireKindTranscriptZstd} {
		packed, err := openObject(dek, blob, id, label)
		if err != nil {
			continue
		}
		decode := decompressGzipPlaintext
		if label == wireKindTranscriptZstd {
			decode = decompressObjectPlaintext
		}
		plaintext, err := decode(packed)
		if err != nil {
			return nil, true, ipc.WrapError(ipc.CodeDecrypt, "同步对象压缩数据不合法", err)
		}
		return plaintext, true, nil
	}
	return nil, false, nil
}
