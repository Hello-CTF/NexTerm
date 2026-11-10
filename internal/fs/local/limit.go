package local

import (
	"io"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// LimitWriter returns a writer that fails with base.ErrTransferTooLarge once more than limit bytes would be
// written; the bytes up to the limit still reach out. A limit <= 0 returns out unchanged.
func LimitWriter(out io.Writer, limit int64) io.Writer {
	if limit <= 0 {
		return out
	}
	return &limitWriter{out: out, remaining: limit}
}

type limitWriter struct {
	out       io.Writer
	remaining int64
}

func (w *limitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) <= w.remaining {
		n, err := w.out.Write(p)
		w.remaining -= int64(n)
		return n, err
	}
	n, err := w.out.Write(p[:w.remaining])
	w.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	return n, base.ErrTransferTooLarge
}
