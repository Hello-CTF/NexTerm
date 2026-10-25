package sync

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

var updateGolden = flag.Bool("update-sync-golden", false, "regenerate testdata/compressed_transcript_golden.json")

const goldenPath = "testdata/compressed_transcript_golden.json"

// goldenVector is decoded by src/test/auth-compressed-object.test.ts, so both implementations read the same bytes.
type goldenVector struct {
	DEKHex        string `json:"dekHex"`
	ID            string `json:"id"`
	Compressed    string `json:"compressedBlobB64"`
	Packed        string `json:"packedBlobB64"`
	Legacy        string `json:"legacyBlobB64"`
	PlaintextB64  string `json:"plaintextB64"`
	CompressedLbl string `json:"compressedLabel"`
	PackedLbl     string `json:"packedLabel"`
}

func gzipFrame(t *testing.T, declared uint64, body []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	out.WriteString(compressedMagic)
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], declared)
	out.Write(size[:])
	w := gzip.NewWriter(&out)
	if _, err := w.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestGzipTranscriptRejectsBombsAndBadHeaders(t *testing.T) {
	dek := vault.GenerateUserDEK()
	body := bytes.Repeat([]byte("A"), 8<<20)
	good := gzipFrame(t, uint64(len(body)), body)
	cases := map[string][]byte{
		"declared smaller than actual": gzipFrame(t, 1<<20, body),
		"declared larger than actual":  gzipFrame(t, uint64(len(body))+1, body),
		"declared over absolute cap":   gzipFrame(t, uint64(compressedMaxDecoded)+1, body),
		"declared zero":                gzipFrame(t, 0, body),
		"bad magic":                    append([]byte("XXXX"), good[4:]...),
		"truncated stream":             good[:compressedHeaderSize+10],
		"header only":                  good[:compressedHeaderSize],
		"not gzip":                     append(append([]byte{}, good[:compressedHeaderSize]...), []byte("not a gzip stream")...),
	}
	for name, packed := range cases {
		blob, err := sealObject(dek, packed, "tr-1", wireKindTranscriptGzip)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tryOpenObject(dek, blob, "tr-1"); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
	blob, err := sealObject(dek, good, "tr-1", wireKindTranscriptGzip)
	if err != nil {
		t.Fatal(err)
	}
	if _, plaintext, err := tryOpenObject(dek, blob, "tr-1"); err != nil || len(plaintext) != len(body) {
		t.Fatalf("valid gzip frame rejected: %v", err)
	}
}

// TestCompressionWriterMatrix pins which labels the writer emits and which readers can open them.
func TestCompressionWriterMatrix(t *testing.T) {
	dek := vault.GenerateUserDEK()
	big := transcriptJSON(t, terminalCorpus(64<<10))
	tiny := []byte(`{"id":"tr-1"}`)
	cases := []struct {
		name     string
		enabled  bool
		kind     string
		payload  []byte
		wantWire string
	}{
		{"off transcript", false, KindTranscript, big, KindTranscript},
		{"on transcript", true, KindTranscript, big, wireKindTranscriptPack},
		{"on tiny transcript", true, KindTranscript, tiny, KindTranscript},
		{"on asset", true, KindAsset, big, KindAsset},
		{"off asset", false, KindAsset, big, KindAsset},
	}
	for _, c := range cases {
		blob, err := sealObjectWire(dek, c.payload, "x-1", c.kind, c.enabled)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if _, err := openObject(dek, blob, "x-1", c.wantWire); err != nil {
			t.Errorf("%s: expected wire label %q: %v", c.name, c.wantWire, err)
		}
		// Old readers know only the kind enum: they open exactly the uncompressed cases.
		_, _, legacyErr := legacyTryOpen(dek, blob, "x-1")
		if (c.wantWire == wireKindTranscriptPack) == (legacyErr == nil) {
			t.Errorf("%s: legacy reader outcome wrong (err=%v)", c.name, legacyErr)
		}
		kind, plaintext, err := tryOpenObject(dek, blob, "x-1")
		if err != nil || kind != c.kind || !bytes.Equal(plaintext, c.payload) {
			t.Errorf("%s: current reader round trip failed: kind=%q err=%v", c.name, kind, err)
		}
	}
}

// TestZstdLabelStillReadable keeps objects written by builds that emitted the zstd label readable by Go.
func TestZstdLabelStillReadable(t *testing.T) {
	dek := vault.GenerateUserDEK()
	plaintext := transcriptJSON(t, terminalCorpus(64<<10))
	packed, err := compressObjectPlaintextZstd(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := sealObject(dek, packed, "tr-1", wireKindTranscriptZstd)
	if err != nil {
		t.Fatal(err)
	}
	kind, opened, err := tryOpenObject(dek, blob, "tr-1")
	if err != nil || kind != KindTranscript || !bytes.Equal(opened, plaintext) {
		t.Fatalf("zstd label: kind=%q err=%v", kind, err)
	}
}

func TestCompressedTranscriptGoldenVector(t *testing.T) {
	if *updateGolden {
		dek := bytes.Repeat([]byte{0x42}, 32)
		plaintext := edgeTranscriptJSON(t, 8<<10)
		gzipped, err := compressObjectPlaintext(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		compressed, err := sealObject(dek, gzipped, "tr-golden", wireKindTranscriptGzip)
		if err != nil {
			t.Fatal(err)
		}
		packed, err := sealObjectWire(dek, plaintext, "tr-golden", KindTranscript, true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := openObject(dek, packed, "tr-golden", wireKindTranscriptPack); err != nil {
			t.Fatalf("golden writer output is not packed: %v", err)
		}
		legacy, err := sealObjectWire(dek, plaintext, "tr-golden", KindTranscript, false)
		if err != nil {
			t.Fatal(err)
		}
		vec := goldenVector{
			DEKHex: hex.EncodeToString(dek), ID: "tr-golden", CompressedLbl: wireKindTranscriptGzip, PackedLbl: wireKindTranscriptPack,
			Compressed: base64.StdEncoding.EncodeToString(compressed), Packed: base64.StdEncoding.EncodeToString(packed),
			Legacy:       base64.StdEncoding.EncodeToString(legacy),
			PlaintextB64: base64.StdEncoding.EncodeToString(plaintext),
		}
		encoded, err := json.MarshalIndent(vec, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.FromSlash(goldenPath), append(encoded, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.FromSlash(goldenPath))
	if err != nil {
		t.Fatal(err)
	}
	var vec goldenVector
	if err := json.Unmarshal(raw, &vec); err != nil {
		t.Fatal(err)
	}
	dek, _ := hex.DecodeString(vec.DEKHex)
	plaintext, _ := base64.StdEncoding.DecodeString(vec.PlaintextB64)
	for name, field := range map[string]string{"compressed": vec.Compressed, "packed": vec.Packed, "legacy": vec.Legacy} {
		blob, _ := base64.StdEncoding.DecodeString(field)
		kind, opened, err := tryOpenObject(dek, blob, vec.ID)
		if err != nil || kind != KindTranscript || !bytes.Equal(opened, plaintext) {
			t.Fatalf("golden %s no longer decodes: kind=%q err=%v", name, kind, err)
		}
	}
}
