package sync

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/vault"
)

// edgeTranscriptJSON covers what the pack layout must reproduce byte for byte: escaped and non-ASCII metadata,
// text that looks like a slot inside string values, and chunks whose data is null or empty.
func edgeTranscriptJSON(t testing.TB, size int) []byte {
	t.Helper()
	raw := terminalCorpus(size)
	object := transcriptObject{
		ID: "tr-1", AssetID: "a", AssetName: "web \"data\":\"x\" <&> 主机  ", AssetKind: "ssh",
		StartedAt: 1, EndedAt: 2, Bytes: int64(len(raw)), Chunks: 4,
		Content: []transcriptChunkObject{
			{Seq: 1, TabID: `t"data":"`, TS: 1, Data: raw[:size/3]},
			{Seq: 2, TabID: "t", TS: 2, Data: nil},
			{Seq: 3, TabID: "t", TS: 3, Data: []byte{}},
			{Seq: 4, TabID: "t", TS: 4, Kind: 2, Data: raw[size/3:]},
		},
	}
	payload, err := marshalObject(object)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestPackedTranscriptRebuildsExactJSON(t *testing.T) {
	dek := vault.GenerateUserDEK()
	for name, plaintext := range map[string][]byte{
		"edge cases": edgeTranscriptJSON(t, 32<<10),
		"terminal":   transcriptJSON(t, terminalCorpus(256<<10)),
	} {
		blob, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript)
		if err != nil {
			t.Fatal(name, err)
		}
		if _, err := openObject(dek, blob, "tr-1", wireKindTranscriptPack); err != nil {
			t.Fatalf("%s: writer did not emit the pack label: %v", name, err)
		}
		kind, opened, err := tryOpenObject(dek, blob, "tr-1")
		if err != nil || kind != KindTranscript || !bytes.Equal(opened, plaintext) {
			t.Fatalf("%s: rebuilt JSON differs: kind=%q err=%v", name, kind, err)
		}
	}
}

func TestPackFallsBackToJSONWhenNotExact(t *testing.T) {
	dek := vault.GenerateUserDEK()
	padding := strings.Repeat("x", 2048)
	for name, plaintext := range map[string][]byte{
		"no chunk data":        []byte(`{"id":"tr-1","assetName":"` + padding + `","contentOmitted":true}`),
		"data is not base64":   []byte(`{"id":"tr-1","content":[{"seq":1,"data":"not base64!"}],"note":"` + padding + `"}`),
		"non-canonical base64": []byte(`{"id":"tr-1","content":[{"seq":1,"data":"QR=="}],"note":"` + padding + `"}`),
	} {
		blob, err := sealObjectWire(dek, plaintext, "tr-1", KindTranscript)
		if err != nil {
			t.Fatal(name, err)
		}
		opened, err := openObject(dek, blob, "tr-1", KindTranscript)
		if err != nil || !bytes.Equal(opened, plaintext) {
			t.Fatalf("%s: expected the unchanged JSON under the transcript label: %v", name, err)
		}
	}
}

type packBody struct {
	skeleton string
	slots    int
	lengths  []uint64
	data     []byte
	magic    string
}

func (b packBody) bytes() []byte {
	out := []byte(b.magic)
	out = binary.AppendUvarint(out, uint64(len(b.skeleton)))
	out = append(out, b.skeleton...)
	out = binary.AppendUvarint(out, uint64(b.slots))
	for _, length := range b.lengths {
		out = binary.AppendUvarint(out, length)
	}
	return append(out, b.data...)
}

func TestPackedTranscriptRejectsMalformedBodies(t *testing.T) {
	dek := vault.GenerateUserDEK()
	valid := packBody{magic: packMagic, skeleton: `{"content":[{"data":""},{"data":""}]}`, slots: 2, lengths: []uint64{3, 2}, data: []byte("abcde")}
	if got, err := unpackTranscriptBody(valid.bytes()); err != nil || string(got) != `{"content":[{"data":"YWJj"},{"data":"ZGU="}]}` {
		t.Fatalf("valid body: %q %v", got, err)
	}
	mutate := func(change func(*packBody)) []byte {
		body := valid
		body.lengths = append([]uint64(nil), valid.lengths...)
		change(&body)
		return body.bytes()
	}
	cases := map[string][]byte{
		"bad magic":              mutate(func(b *packBody) { b.magic = "XXXX" }),
		"no slots":               mutate(func(b *packBody) { b.slots, b.lengths, b.data = 0, nil, nil }),
		"more slots than body":   mutate(func(b *packBody) { b.slots = 1 << 40 }),
		"fewer slots declared":   mutate(func(b *packBody) { b.slots, b.lengths, b.data = 1, []uint64{3}, []byte("abc") }),
		"more slots declared":    mutate(func(b *packBody) { b.slots, b.lengths, b.data = 3, []uint64{3, 2, 1}, []byte("abcdef") }),
		"lengths exceed data":    mutate(func(b *packBody) { b.lengths[1] = 9 }),
		"trailing data":          mutate(func(b *packBody) { b.data = append(b.data, 'z') }),
		"filled slot":            mutate(func(b *packBody) { b.skeleton = `{"content":[{"data":"x"},{"data":""}]}` }),
		"skeleton past the body": append([]byte(packMagic), binary.AppendUvarint(nil, 1<<20)...),
		"truncated":              valid.bytes()[:8],
	}
	for name, body := range cases {
		blob, err := sealObject(dek, rawFrame(t, uint64(len(body)), body), "tr-1", wireKindTranscriptPack)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := tryOpenObject(dek, blob, "tr-1"); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func gzipSize(t testing.TB, plaintext []byte) int {
	t.Helper()
	var out bytes.Buffer
	writer := gzip.NewWriter(&out)
	if _, err := writer.Write(plaintext); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Len()
}

// TestPackedTranscriptSmallerThanGzipJSON pins the reason for the format: packing raw bytes into one zstd stream must
// beat gzip over the base64 JSON, most of all when a long session repeats output beyond gzip's 32 KiB window.
func TestPackedTranscriptSmallerThanGzipJSON(t *testing.T) {
	session := terminalCorpus(1 << 20)
	repeated := bytes.Repeat(session[:256<<10], 4)
	for name, raw := range map[string][]byte{"varied": session, "repetitive": repeated} {
		plaintext := transcriptJSON(t, raw)
		packed, err := packTranscriptPlaintext(plaintext)
		if err != nil || packed == nil {
			t.Fatalf("%s: not packed: %v", name, err)
		}
		gzipped := gzipSize(t, plaintext)
		t.Logf("%s: raw=%d json=%d gzip-json=%d (%.1f%%) pack=%d (%.1f%%)", name, len(raw), len(plaintext),
			gzipped, 100*float64(gzipped)/float64(len(raw)), len(packed), 100*float64(len(packed))/float64(len(raw)))
		if len(packed) >= gzipped {
			t.Fatalf("%s: pack %d is not smaller than gzip JSON %d", name, len(packed), gzipped)
		}
	}
}
