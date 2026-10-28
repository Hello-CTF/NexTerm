package sync

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
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
// wireKindTranscriptPack is what the writer emits. Compressing the logical JSON wastes most of the gain because chunk
// bytes travel as base64, which hides their redundancy, so the pack layout splits the JSON into a skeleton whose chunk
// "data" strings are emptied and the decoded chunk bytes stored back to back, and compresses the whole body as one
// zstd stream. Readers refill each emptied string with the base64 of its bytes and get the original JSON byte for
// byte. wireKindTranscriptGzip compresses the JSON itself.
//
// Sealed plaintext layout: "NXZ1" | uint64 big-endian decoded size | zstd frame (gzip stream for the gzip label).
// The decoded pack body is "NXP1" | uvarint skeleton length | skeleton | uvarint slot count | one uvarint byte length
// per slot | the slot bytes concatenated; every other label decodes straight to the logical JSON.
const (
	wireKindTranscriptPack = "transcript.zpack"
	wireKindTranscriptGzip = "transcript.gzip"

	packMagic = "NXP1"
	// packWindowSize bounds the zstd history a reader must hold; repeated prompts and command output within a
	// window this size are what make the single stream smaller than per-block compression.
	packWindowSize = 8 << 20

	compressedMagic      = "NXZ1"
	compressedHeaderSize = len(compressedMagic) + 8
	// compressMinBytes: below this the zstd frame overhead is not worth a format change.
	compressMinBytes = 1 << 10
	// compressedMaxDecoded is the absolute decode cap, aligned with the largest accepted plaintext.
	compressedMaxDecoded = maxApplyObjectBytes
)

// Transcripts are always written compressed: the Go engine packs them as "transcript.zpack" and the browser writes
// "transcript.gzip". Only payloads too small or not shaped for packing keep the plain transcript label. Devices that
// predate the compressed readers cannot open these objects; they quarantine them with a visible decrypt failure,
// never an empty transcript.

var (
	errCompressedFormat = errors.New("invalid compressed sync object")

	gzipWriters stdsync.Pool

	zstdEncoders stdsync.Pool
	zstdDecoders stdsync.Pool
	packEncoders stdsync.Pool

	// packSlotPrefix only matches a "data" key followed by a string value: inside JSON strings quotes are escaped,
	// so the pattern cannot occur within a value.
	packSlotPrefix = []byte(`"data":"`)
)

func acquirePackEncoder() (*zstd.Encoder, error) {
	if pooled, ok := packEncoders.Get().(*zstd.Encoder); ok {
		return pooled, nil
	}
	return zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1), zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithWindowSize(packWindowSize))
}

// packTranscriptPlaintext returns the pack layout, or nil when the payload cannot be packed exactly or packing is
// not worthwhile; the caller then seals the logical JSON unchanged.
func packTranscriptPlaintext(plaintext []byte) ([]byte, error) {
	if len(plaintext) < compressMinBytes || len(plaintext) > compressedMaxDecoded {
		return nil, nil
	}
	skeleton := make([]byte, 0, len(plaintext)/8)
	data := make([]byte, 0, len(plaintext)*3/4)
	var lengths []int
	rest := plaintext
	for {
		at := bytes.Index(rest, packSlotPrefix)
		if at < 0 {
			skeleton = append(skeleton, rest...)
			break
		}
		start := at + len(packSlotPrefix)
		end := bytes.IndexByte(rest[start:], '"')
		if end < 0 {
			return nil, nil
		}
		decoded, err := base64.StdEncoding.AppendDecode(data, rest[start:start+end])
		if err != nil {
			return nil, nil
		}
		lengths = append(lengths, len(decoded)-len(data))
		data = decoded
		skeleton = append(skeleton, rest[:start]...)
		rest = rest[start+end:]
	}
	if len(lengths) == 0 {
		return nil, nil
	}
	body := make([]byte, 0, len(packMagic)+len(skeleton)+len(data)+(len(lengths)+2)*binary.MaxVarintLen64)
	body = append(body, packMagic...)
	body = binary.AppendUvarint(body, uint64(len(skeleton)))
	body = append(body, skeleton...)
	body = binary.AppendUvarint(body, uint64(len(lengths)))
	for _, length := range lengths {
		body = binary.AppendUvarint(body, uint64(length))
	}
	body = append(body, data...)
	// The receiver's rebuild is the contract; anything that would not reproduce the exact bytes stays unpacked.
	if rebuilt, err := unpackTranscriptBody(body); err != nil || !bytes.Equal(rebuilt, plaintext) {
		return nil, nil
	}

	encoder, err := acquirePackEncoder()
	if err != nil {
		return nil, ipc.WrapError(ipc.CodeInternal, "无法压缩同步对象", err)
	}
	defer packEncoders.Put(encoder)
	header := make([]byte, compressedHeaderSize, compressedHeaderSize+len(body)/4)
	copy(header, compressedMagic)
	binary.BigEndian.PutUint64(header[len(compressedMagic):], uint64(len(body)))
	packed := encoder.EncodeAll(body, header)
	if len(packed) >= len(plaintext) {
		return nil, nil
	}
	return packed, nil
}

// unpackTranscriptBody rebuilds the logical JSON from a decoded pack body. The rebuilt size is checked against the
// decode cap before anything is allocated for it.
func unpackTranscriptBody(body []byte) ([]byte, error) {
	if !bytes.HasPrefix(body, []byte(packMagic)) {
		return nil, errCompressedFormat
	}
	rest := body[len(packMagic):]
	readLength := func() (int, bool) {
		value, n := binary.Uvarint(rest)
		if n <= 0 || value > uint64(len(body)) {
			return 0, false
		}
		rest = rest[n:]
		return int(value), true
	}
	skeletonLength, ok := readLength()
	if !ok || skeletonLength > len(rest) {
		return nil, errCompressedFormat
	}
	skeleton := rest[:skeletonLength]
	rest = rest[skeletonLength:]
	// Every slot needs at least one length byte, which bounds the allocation below by the body size.
	slots, ok := readLength()
	if !ok || slots == 0 || slots > len(rest) {
		return nil, errCompressedFormat
	}
	lengths := make([]int, slots)
	total := len(skeleton)
	dataBytes := 0
	for i := range lengths {
		if lengths[i], ok = readLength(); !ok {
			return nil, errCompressedFormat
		}
		dataBytes += lengths[i]
		total += base64.StdEncoding.EncodedLen(lengths[i])
		if dataBytes > len(rest) || total > compressedMaxDecoded {
			return nil, errCompressedFormat
		}
	}
	if dataBytes != len(rest) {
		return nil, errCompressedFormat
	}
	out := make([]byte, 0, total)
	for _, length := range lengths {
		at := bytes.Index(skeleton, packSlotPrefix)
		if at < 0 || len(skeleton) <= at+len(packSlotPrefix) || skeleton[at+len(packSlotPrefix)] != '"' {
			return nil, errCompressedFormat
		}
		out = append(out, skeleton[:at+len(packSlotPrefix)]...)
		out = base64.StdEncoding.AppendEncode(out, rest[:length])
		rest = rest[length:]
		skeleton = skeleton[at+len(packSlotPrefix):]
	}
	if bytes.Contains(skeleton, packSlotPrefix) {
		return nil, errCompressedFormat
	}
	return append(out, skeleton...), nil
}

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

// compressObjectPlaintext returns the gzip layout, or nil when compression is not worthwhile. The writer no longer
// uses it; it exists for interop tests.
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

// sealObjectWire seals an object, compressing transcript content whenever packing applies.
func sealObjectWire(dek, plaintext []byte, id, kind string) ([]byte, error) {
	if kind == KindTranscript {
		packed, err := packTranscriptPlaintext(plaintext)
		if err != nil {
			return nil, err
		}
		if packed != nil {
			return sealObject(dek, packed, id, wireKindTranscriptPack)
		}
	}
	return sealObject(dek, plaintext, id, kind)
}

// openCompressedTranscript tries the compressed labels and returns the logical transcript JSON.
func openCompressedTranscript(dek, blob []byte, id string) ([]byte, bool, error) {
	for _, label := range []string{wireKindTranscriptPack, wireKindTranscriptGzip} {
		packed, err := openObject(dek, blob, id, label)
		if err != nil {
			continue
		}
		decode := decompressGzipPlaintext
		switch label {
		case wireKindTranscriptPack:
			decode = decompressPackedTranscript
		}
		plaintext, err := decode(packed)
		if err != nil {
			return nil, true, ipc.WrapError(ipc.CodeDecrypt, "同步对象压缩数据不合法", err)
		}
		return plaintext, true, nil
	}
	return nil, false, nil
}

func decompressPackedTranscript(data []byte) ([]byte, error) {
	body, err := decompressObjectPlaintext(data)
	if err != nil {
		return nil, err
	}
	return unpackTranscriptBody(body)
}
