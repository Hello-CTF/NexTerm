package terminal

import (
	"golang.org/x/text/transform"
)

// Transcoder incrementally converts a remote byte stream into UTF-8 for
// the VT screen and text consumers. Multibyte characters split across
// chunks are buffered (pending) instead of being replaced, and invalid
// bytes become U+FFFD rather than being dropped.
//
// Transcoder is stateful and not safe for concurrent use.
type Transcoder struct {
	enc     Encoding
	dec     transformer
	pending []byte
}

type transformer interface {
	Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error)
	Reset()
}

// NewTranscoder creates a transcoder for enc.
func NewTranscoder(enc Encoding) *Transcoder {
	return &Transcoder{enc: enc, dec: enc.decoder()}
}

// Encoding returns the current source encoding.
func (t *Transcoder) Encoding() Encoding { return t.enc }

// Switch changes the source encoding at runtime. Any buffered partial
// character is discarded: subsequent bytes are interpreted in the new
// encoding.
func (t *Transcoder) Switch(enc Encoding) {
	if enc == t.enc {
		return
	}
	t.enc = enc
	t.dec = enc.decoder()
	t.pending = nil
}

// Feed decodes p and returns the UTF-8 output produced so far. An
// incomplete trailing character is held back until the next Feed or Flush.
func (t *Transcoder) Feed(p []byte) []byte {
	if len(t.pending) > 0 {
		merged := make([]byte, 0, len(t.pending)+len(p))
		merged = append(merged, t.pending...)
		merged = append(merged, p...)
		p = merged
		t.pending = nil
	}
	return t.decode(p, false)
}

// Flush decodes any buffered partial character as of end-of-stream,
// emitting U+FFFD for it. The transcoder remains usable afterwards.
func (t *Transcoder) Flush() []byte {
	if len(t.pending) == 0 {
		return nil
	}
	p := t.pending
	t.pending = nil
	return t.decode(p, true)
}

func (t *Transcoder) decode(p []byte, atEOF bool) []byte {
	out := make([]byte, 0, len(p)+8)
	if len(p) == 0 {
		return out
	}
	dst := make([]byte, 3*len(p)+16)
	for len(p) > 0 {
		nDst, nSrc, err := t.dec.Transform(dst, p, atEOF)
		out = append(out, dst[:nDst]...)
		p = p[nSrc:]
		switch err {
		case nil:
			if nSrc == 0 && nDst == 0 {
				// No progress without an error should not happen;
				// avoid spinning if a decoder misbehaves.
				return out
			}
		case transform.ErrShortDst:
			// Reuse dst; more output space is available after append.
		case transform.ErrShortSrc:
			// Incomplete trailing character: keep it for next time.
			if !atEOF {
				t.pending = append(t.pending[:0], p...)
				return out
			}
			// At EOF a decoder should replace, not report short
			// source; drop nothing if one still does.
			out = append(out, "�"...)
			return out
		default:
			// Decoders substitute U+FFFD for invalid bytes themselves;
			// any other error is treated as end of decodable input.
			return out
		}
	}
	return out
}

// decodeAll performs a one-shot decode of a complete byte slice, replacing
// any trailing partial character. Used for ring dumps, which may start or
// end in the middle of a multibyte character.
func decodeAll(enc Encoding, p []byte) []byte {
	t := NewTranscoder(enc)
	out := t.Feed(p)
	return append(out, t.Flush()...)
}
