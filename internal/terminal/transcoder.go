package terminal

import (
	"golang.org/x/text/transform"
)

type Transcoder struct {
	enc     Encoding
	dec     transformer
	pending []byte
}

type transformer interface {
	Transform(dst, src []byte, atEOF bool) (nDst, nSrc int, err error)
	Reset()
}

func NewTranscoder(enc Encoding) *Transcoder {
	return &Transcoder{enc: enc, dec: enc.decoder()}
}

func (t *Transcoder) Encoding() Encoding { return t.enc }

func (t *Transcoder) Switch(enc Encoding) {
	if enc == t.enc {
		return
	}
	t.enc = enc
	t.dec = enc.decoder()
	t.pending = nil
}

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
				return out
			}
		case transform.ErrShortDst:
		case transform.ErrShortSrc:
			if !atEOF {
				t.pending = append(t.pending[:0], p...)
				return out
			}
			out = append(out, "�"...)
			return out
		default:
			return out
		}
	}
	return out
}

func decodeAll(enc Encoding, p []byte) []byte {
	t := NewTranscoder(enc)
	out := t.Feed(p)
	return append(out, t.Flush()...)
}
