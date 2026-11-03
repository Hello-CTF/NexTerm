// Package asciicast streams terminal output as an asciicast v2 recording.
package asciicast

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
	// estimatedGapMS spaces events whose source timestamp is missing.
	estimatedGapMS = 100
)

// Header describes the recording. StartMS is the wall-clock base in Unix milliseconds;
// event times are written relative to it.
type Header struct {
	Width   int
	Height  int
	StartMS int64
	Title   string
}

// Result summarizes a finished recording.
type Result struct {
	Bytes           int64
	Events          int64
	TimingEstimated bool
}

type Writer struct {
	out     io.Writer
	startMS int64
	lastMS  int64 // offset of the previous event from startMS
	pending []byte
	result  Result
	closed  bool
	lineBuf bytes.Buffer
	jsonBuf bytes.Buffer
	jsonEnc *json.Encoder
}

func NewWriter(out io.Writer, header Header) (*Writer, error) {
	if header.Width <= 0 || header.Height <= 0 {
		header.Width, header.Height = defaultWidth, defaultHeight
	}
	w := &Writer{out: out, startMS: header.StartMS}
	w.jsonEnc = json.NewEncoder(&w.jsonBuf)
	w.jsonEnc.SetEscapeHTML(false)
	meta := map[string]any{
		"version":   2,
		"width":     header.Width,
		"height":    header.Height,
		"timestamp": header.StartMS / 1000,
	}
	if header.Title != "" {
		meta["title"] = header.Title
	}
	line, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	if err := w.writeLine(append(line, '\n')); err != nil {
		return nil, err
	}
	return w, nil
}

// Output records terminal output. Input must never be passed here.
func (w *Writer) Output(tsMS int64, data []byte) error {
	if w.closed {
		return errors.New("asciicast: writer closed")
	}
	offset := w.offset(tsMS)
	text := w.decode(data)
	if text == "" {
		return nil
	}
	return w.event(offset, "o", text)
}

func (w *Writer) Resize(tsMS int64, cols, rows int) error {
	if w.closed {
		return errors.New("asciicast: writer closed")
	}
	if cols <= 0 || rows <= 0 {
		return nil
	}
	return w.event(w.offset(tsMS), "r", fmt.Sprintf("%dx%d", cols, rows))
}

// Close flushes a trailing incomplete UTF-8 sequence as replacement characters.
func (w *Writer) Close() (Result, error) {
	if w.closed {
		return w.result, nil
	}
	w.closed = true
	if len(w.pending) > 0 {
		text := strings.Repeat("�", len(w.pending))
		w.pending = nil
		if err := w.event(w.lastMS, "o", text); err != nil {
			return w.result, err
		}
	}
	return w.result, nil
}

// offset maps a wall-clock timestamp to a monotonic offset from the base.
func (w *Writer) offset(tsMS int64) int64 {
	if tsMS <= 0 {
		w.result.TimingEstimated = true
		w.lastMS += estimatedGapMS
		return w.lastMS
	}
	offset := tsMS - w.startMS
	if offset < w.lastMS {
		offset = w.lastMS
	}
	w.lastMS = offset
	return offset
}

// decode converts bytes to valid UTF-8, holding back an incomplete tail for the next call.
func (w *Writer) decode(data []byte) string {
	buf := data
	if len(w.pending) > 0 {
		buf = append(w.pending, data...)
		w.pending = nil
	}
	var out strings.Builder
	for i := 0; i < len(buf); {
		if !utf8.FullRune(buf[i:]) {
			w.pending = append([]byte(nil), buf[i:]...)
			break
		}
		r, size := utf8.DecodeRune(buf[i:])
		if r == utf8.RuneError && size == 1 {
			out.WriteString("�")
		} else {
			out.Write(buf[i : i+size])
		}
		i += size
	}
	return out.String()
}

func (w *Writer) event(offsetMS int64, code, data string) error {
	w.jsonBuf.Reset()
	if err := w.jsonEnc.Encode(data); err != nil {
		return err
	}
	encoded := bytes.TrimRight(w.jsonBuf.Bytes(), "\n")
	w.lineBuf.Reset()
	fmt.Fprintf(&w.lineBuf, "[%d.%03d,\"%s\",", offsetMS/1000, offsetMS%1000, code)
	w.lineBuf.Write(encoded)
	w.lineBuf.WriteString("]\n")
	if err := w.writeLine(w.lineBuf.Bytes()); err != nil {
		return err
	}
	w.result.Events++
	return nil
}

func (w *Writer) writeLine(line []byte) error {
	n, err := w.out.Write(line)
	w.result.Bytes += int64(n)
	return err
}
