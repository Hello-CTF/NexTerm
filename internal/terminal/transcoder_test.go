package terminal

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
)

func encodeForTest(t *testing.T, enc encoding.Encoding, s string) []byte {
	t.Helper()
	out, err := enc.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("encode %q: %v", s, err)
	}
	return out
}

// feedAll feeds src through tr with the given chunk size and collects the
// decoded output.
func feedAll(tr *Transcoder, src []byte, chunk int) []byte {
	var out []byte
	for len(src) > 0 {
		n := chunk
		if n > len(src) {
			n = len(src)
		}
		out = append(out, tr.Feed(src[:n])...)
		src = src[n:]
	}
	return out
}

func TestTranscoderAllEncodingsEverySplit(t *testing.T) {
	cases := []struct {
		name string
		enc  Encoding
		src  encoding.Encoding
		text string
	}{
		{"utf8", UTF8, nil, "中文输出测试 mixed ASCII ✓"},
		{"gbk", GBK, simplifiedchinese.GBK, "中文目录 server-01"},
		{"gb18030", GB18030, simplifiedchinese.GB18030, "服务器编码𠀋extension"},
		{"big5", Big5, traditionalchinese.Big5, "繁體中文測試"},
		{"latin1", Latin1, charmap.Windows1252, "café €99"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(tc.text)
			if tc.src != nil {
				raw = encodeForTest(t, tc.src, tc.text)
			}
			// Whole slice, every fixed chunk size, and every single
			// split position must all reproduce the original text.
			for chunk := 1; chunk <= len(raw)+1; chunk++ {
				tr := NewTranscoder(tc.enc)
				out := feedAll(tr, raw, chunk)
				out = append(out, tr.Flush()...)
				if string(out) != tc.text {
					t.Fatalf("chunk=%d: got %q, want %q", chunk, out, tc.text)
				}
			}
			for split := 0; split <= len(raw); split++ {
				tr := NewTranscoder(tc.enc)
				out := tr.Feed(raw[:split])
				out = append(out, tr.Feed(raw[split:])...)
				out = append(out, tr.Flush()...)
				if string(out) != tc.text {
					t.Fatalf("split=%d: got %q, want %q", split, out, tc.text)
				}
			}
		})
	}
}

func TestTranscoderInvalidByteReplacedNotDropped(t *testing.T) {
	tr := NewTranscoder(UTF8)
	out := tr.Feed([]byte{'a', 0xFF, 'b'})
	if string(out) != "a�b" {
		t.Fatalf("got %q", out)
	}
}

func TestTranscoderSwitchMidstream(t *testing.T) {
	tr := NewTranscoder(GBK)
	raw := encodeForTest(t, simplifiedchinese.GBK, "部分")
	_ = tr.Feed(raw)
	tr.Switch(UTF8)
	if out := tr.Feed([]byte("切换编码")); string(out) != "切换编码" {
		t.Fatalf("got %q", out)
	}
	if tr.Encoding() != UTF8 {
		t.Fatalf("encoding = %v", tr.Encoding())
	}
}

func TestTranscoderSwitchDropsPending(t *testing.T) {
	tr := NewTranscoder(GBK)
	raw := encodeForTest(t, simplifiedchinese.GBK, "中")
	if len(raw) < 2 {
		t.Fatal("expected multibyte encoding")
	}
	if out := tr.Feed(raw[:1]); len(out) != 0 {
		t.Fatalf("partial lead byte should be buffered, got %q", out)
	}
	tr.Switch(UTF8)
	if out := tr.Feed([]byte("ok")); string(out) != "ok" {
		t.Fatalf("pending from old encoding leaked: %q", out)
	}
}

func TestTranscoderFlushReplacesTrailingPartial(t *testing.T) {
	tr := NewTranscoder(UTF8)
	raw := []byte("中")
	out := tr.Feed(raw[:1])
	if len(out) != 0 {
		t.Fatalf("expected buffered partial, got %q", out)
	}
	if flushed := tr.Flush(); !bytes.Contains(flushed, []byte("�")) {
		t.Fatalf("flush = %q, want replacement char", flushed)
	}
}

func TestParseEncoding(t *testing.T) {
	for name, want := range map[string]Encoding{
		"utf-8": UTF8, "UTF8": UTF8, "gbk": GBK, "GB18030": GB18030,
		"big5": Big5, "latin1": Latin1, "ISO-8859-1": Latin1,
	} {
		got, err := ParseEncoding(name)
		if err != nil || got != want {
			t.Fatalf("ParseEncoding(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := ParseEncoding("koi8-r"); err == nil {
		t.Fatal("expected error for unknown encoding")
	}
}
