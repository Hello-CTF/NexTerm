package sync

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// terminalCorpus builds deterministic, terminal-like output (log lines with varying numbers and some noise).
func terminalCorpus(size int) []byte {
	rng := rand.New(rand.NewSource(7))
	var buf bytes.Buffer
	for buf.Len() < size {
		fmt.Fprintf(&buf, "\x1b[32m2026-10-25T10:%02d:%02d.%03dZ\x1b[0m INFO worker-%d handled request id=%08x path=/api/v1/items/%d status=200 bytes=%d\r\n",
			rng.Intn(60), rng.Intn(60), rng.Intn(1000), rng.Intn(16), rng.Uint32(), rng.Intn(5000), rng.Intn(100000))
		if rng.Intn(20) == 0 {
			noise := make([]byte, 24)
			rng.Read(noise)
			buf.Write(noise)
		}
	}
	return buf.Bytes()[:size]
}

func transcriptJSON(t testing.TB, raw []byte) []byte {
	t.Helper()
	object := transcriptObject{ID: "tr-1", AssetID: "a", AssetName: "web", AssetKind: "ssh", StartedAt: 1, EndedAt: 2, Bytes: int64(len(raw)), Chunks: int64(len(raw)/4096 + 1)}
	for i := 0; i < len(raw); i += 4096 {
		end := min(i+4096, len(raw))
		object.Content = append(object.Content, transcriptChunkObject{Seq: int64(i / 4096), TabID: "t", TS: 1, Data: raw[i:end]})
	}
	payload, err := marshalObject(object)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestCompressedTranscriptRoundTripAndLegacy(t *testing.T) {
	dek := vault.GenerateUserDEK()
	plaintext := transcriptJSON(t, terminalCorpus(256<<10))

	compressed, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, true)
	if err != nil {
		t.Fatal(err)
	}
	kind, opened, err := tryOpenObject(dek, compressed, "tr-1")
	if err != nil || kind != KindTranscript || !bytes.Equal(opened, plaintext) {
		t.Fatalf("compressed round trip: kind=%q err=%v equal=%v", kind, err, bytes.Equal(opened, plaintext))
	}
	if _, err := openObject(dek, compressed, "tr-1", KindTranscript); err == nil {
		t.Fatal("compressed object must not open under the legacy transcript label")
	}

	legacy, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, false)
	if err != nil {
		t.Fatal(err)
	}
	kind, opened, err = tryOpenObject(dek, legacy, "tr-1")
	if err != nil || kind != KindTranscript || !bytes.Equal(opened, plaintext) {
		t.Fatalf("legacy round trip: kind=%q err=%v", kind, err)
	}

	// Other kinds and tiny or incompressible transcripts keep the legacy encoding even when compression is on.
	for name, body := range map[string][]byte{"tiny": []byte(`{"id":"tr-1"}`)} {
		blob, err := sealObjectWire(dek, body, "tr-1", KindTranscript, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := openObject(dek, blob, "tr-1", KindTranscript); err != nil {
			t.Fatalf("%s transcript should stay legacy: %v", name, err)
		}
	}
	asset, err := sealObjectWire(dek, plaintext, "a-1", KindAsset, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openObject(dek, asset, "a-1", KindAsset); err != nil {
		t.Fatalf("non-transcript kinds must never be compressed: %v", err)
	}
}

func TestCompressedTranscriptHashIndependentOfEncoding(t *testing.T) {
	dek := vault.GenerateUserDEK()
	plaintext := transcriptJSON(t, terminalCorpus(64<<10))
	var hashes []string
	for _, compress := range []bool{false, true} {
		blob, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, compress)
		if err != nil {
			t.Fatal(err)
		}
		kind, opened, err := tryOpenObject(dek, blob, "tr-1")
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, objectPayloadHash(opened))
		// The canonical apply path must also produce identical bytes.
		canonical, err := canonicalApplyPayload(ApplyObject{ID: "tr-1", Kind: kind, Payload: opened})
		if err != nil {
			t.Fatal(err)
		}
		hashes = append(hashes, objectPayloadHash(canonical))
	}
	for _, h := range hashes {
		if h != hashes[0] {
			t.Fatalf("payload hash depends on wire encoding: %v", hashes)
		}
	}
	local := newCompareEntity("tr-1", KindTranscript, plaintext)
	remote := newCompareEntity("tr-1", KindTranscript, plaintext)
	if compareState(local, remote) != CompareStateSame {
		t.Fatal("same logical content must compare as same")
	}
}

func rawFrame(t *testing.T, declared uint64, body []byte) []byte {
	t.Helper()
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	header := make([]byte, compressedHeaderSize)
	copy(header, compressedMagic)
	binary.BigEndian.PutUint64(header[len(compressedMagic):], declared)
	return encoder.EncodeAll(body, header)
}

func TestDecompressRejectsBombsAndBadHeaders(t *testing.T) {
	dek := vault.GenerateUserDEK()
	body := bytes.Repeat([]byte("A"), 8<<20)
	cases := map[string][]byte{
		"declared smaller than actual": rawFrame(t, 1<<20, body),
		"declared larger than actual":  rawFrame(t, uint64(len(body))+1, body),
		"declared over absolute cap":   rawFrame(t, uint64(compressedMaxDecoded)+1, body),
		"declared zero":                rawFrame(t, 0, body),
		"bad magic":                    append([]byte("XXXX"), rawFrame(t, 8, []byte("12345678"))[4:]...),
		"truncated frame":              rawFrame(t, uint64(len(body)), body)[:compressedHeaderSize+8],
		"header only":                  rawFrame(t, 5, nil)[:compressedHeaderSize],
	}
	for name, packed := range cases {
		blob, err := sealObject(dek, packed, "tr-1", wireKindTranscriptZstd)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tryOpenObject(dek, blob, "tr-1"); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	// A correct declaration of the same body is accepted, so the rejections above are not collateral.
	good, err := sealObject(dek, rawFrame(t, uint64(len(body)), body), "tr-1", wireKindTranscriptZstd)
	if err != nil {
		t.Fatal(err)
	}
	if _, plaintext, err := tryOpenObject(dek, good, "tr-1"); err != nil || len(plaintext) != len(body) {
		t.Fatalf("valid frame rejected: %v", err)
	}
}

// legacyTryOpen reproduces the reader of devices that predate compressed transcripts.
func legacyTryOpen(dek, blob []byte, id string) (string, []byte, error) {
	for _, kind := range objectKinds {
		if plaintext, err := openObject(dek, blob, id, kind); err == nil {
			return kind, plaintext, nil
		}
	}
	return "", nil, fmt.Errorf("undecryptable")
}

func TestLegacyReaderFailsVisiblyOnCompressedTranscript(t *testing.T) {
	dek := vault.GenerateUserDEK()
	plaintext := transcriptJSON(t, terminalCorpus(64<<10))
	blob, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := legacyTryOpen(dek, blob, "tr-1"); err == nil {
		t.Fatal("legacy reader must not decrypt a compressed transcript (it would apply it as empty content)")
	}
}

func TestEngineCompressionGatingAndInterop(t *testing.T) {
	server := newTestSyncServer(t)
	server.createUser(t, "alice", "alice-pw-123")
	ctx := context.Background()
	endedAt := int64(300)
	content := terminalCorpus(512 << 10)

	for _, enabled := range []bool{false, true} {
		deviceA := newTestDevice(t)
		deviceB := newTestDevice(t)
		deviceA.engine.SetTranscriptCompression(enabled)
		id := ids.New()
		putDeviceTranscript(t, deviceA, id, 100, &endedAt, content, true)
		syncDevice(t, deviceA, server, "alice", "alice-pw-123")

		var blob []byte
		if err := server.db.DB().QueryRowContext(ctx, "SELECT blob FROM user_sync_object WHERE id = ?", id).Scan(&blob); err != nil {
			t.Fatal(err)
		}
		dek := mustDEK(t, server, "alice", "alice-pw-123")
		_, _, legacyErr := legacyTryOpen(dek, blob, id)
		if enabled == (legacyErr == nil) {
			t.Fatalf("enabled=%v legacyReaderOpened=%v", enabled, legacyErr == nil)
		}
		if !enabled && DefaultTranscriptCompression {
			t.Fatal("default must be off until every device can read the format")
		}

		// Readers always understand both encodings.
		syncDevice(t, deviceB, server, "alice", "alice-pw-123")
		chunks, err := deviceB.db.TranscriptChunks(ctx, id, 0, 4<<20)
		if err != nil {
			t.Fatal(err)
		}
		var got []byte
		for _, c := range chunks {
			got = append(got, c.Data...)
		}
		if !bytes.Equal(got, content) {
			t.Fatalf("enabled=%v: content mismatch after sync (%d vs %d bytes)", enabled, len(got), len(content))
		}
		// A second round is idle: the hash stays stable regardless of encoding.
		if report := syncDevice(t, deviceB, server, "alice", "alice-pw-123"); report.Pushed != 0 || report.Applied != 0 {
			t.Fatalf("enabled=%v: idle round not idle: %+v", enabled, report)
		}
	}
}

// wireBytes is the JSON size of a pushed object: the Blob field travels as base64 inside JSON.
func wireBytes(blob []byte) int { return (len(blob)+2)/3*4 + len(`{"id":"x","blob":""}`) }

func TestCompressedTranscriptWireBytesContract(t *testing.T) {
	dek := vault.GenerateUserDEK()
	raw := terminalCorpus(4 << 20)
	plaintext := transcriptJSON(t, raw)
	before, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, false)
	if err != nil {
		t.Fatal(err)
	}
	after, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, true)
	if err != nil {
		t.Fatal(err)
	}
	b, a := wireBytes(before), wireBytes(after)
	t.Logf("4 MiB corpus: raw=%d json=%d wire before=%d (%.2fx raw) after=%d (%.2fx raw)", len(raw), len(plaintext), b, float64(b)/float64(len(raw)), a, float64(a)/float64(len(raw)))
	if a*2 >= len(raw) {
		t.Fatalf("compressed wire bytes %d are not below 50%% of raw %d", a, len(raw))
	}
	if a >= b {
		t.Fatalf("compression did not reduce wire bytes: %d >= %d", a, b)
	}
}

func BenchmarkSealTranscript(b *testing.B) {
	dek := vault.GenerateUserDEK()
	raw := terminalCorpus(4 << 20)
	plaintext := transcriptJSON(b, raw)
	for _, compress := range []bool{false, true} {
		b.Run(fmt.Sprintf("compress=%v", compress), func(b *testing.B) {
			b.SetBytes(int64(len(raw)))
			var size int
			for range b.N {
				blob, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript, compress)
				if err != nil {
					b.Fatal(err)
				}
				size = wireBytes(blob)
			}
			b.ReportMetric(float64(size), "wire-B")
		})
	}
}
