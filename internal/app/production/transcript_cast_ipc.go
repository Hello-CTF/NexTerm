package production

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/Hello-CTF/NexTerm/internal/asciicast"
	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const transcriptCastPageBytes = 1 << 20

type transcriptExportCastRequest struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type transcriptExportCastResult struct {
	Path            string `json:"path"`
	Bytes           int64  `json:"bytes"`
	Events          int64  `json:"events"`
	TimingEstimated bool   `json:"timingEstimated"`
}

func (s *terminalCommandService) exportTranscriptCast(ctx context.Context, input transcriptExportCastRequest) (transcriptExportCastResult, error) {
	if err := store.EnsureID(input.ID); err != nil {
		return transcriptExportCastResult{}, err
	}
	row, err := s.database.TranscriptGet(ctx, input.ID)
	if err != nil {
		return transcriptExportCastResult{}, err
	}
	if row.ContentOmitted {
		return transcriptExportCastResult{}, ipc.BadParam(errors.New("该录像只同步了元数据，没有可导出的内容"))
	}
	path, maxBytes, release, err := resolveStagedWriteTarget(ctx, s.stagedBlobs, input.Path)
	if err != nil {
		return transcriptExportCastResult{}, err
	}
	defer release()
	var summary asciicast.Result
	err = atomicfile.Write(path, 0o600, func(out io.Writer) error {
		var encodeErr error
		summary, encodeErr = s.encodeTranscriptCast(ctx, row, fslocal.LimitWriter(out, maxBytes))
		return encodeErr
	})
	if errors.Is(err, base.ErrTransferTooLarge) {
		return transcriptExportCastResult{}, fsIPCError("", err)
	}
	if err != nil {
		return transcriptExportCastResult{}, err
	}
	return transcriptExportCastResult{
		Path: path, Bytes: summary.Bytes, Events: summary.Events, TimingEstimated: summary.TimingEstimated,
	}, nil
}

// encodeTranscriptCast streams the transcript pages into an asciicast writer. The header is written before
// any event, so chunks are held back until the first output (or the end) fixes the initial terminal size.
func (s *terminalCommandService) encodeTranscriptCast(ctx context.Context, row store.TranscriptRow, out io.Writer) (asciicast.Result, error) {
	var (
		writer  *asciicast.Writer
		held    []store.TranscriptChunkRow
		cols    = 0
		rows    = 0
		startMS = row.StartedAt
		first   = true
		// sizeIndex is the held resize chunk that fixed the header size.
		sizeIndex = -1
	)
	openWriter := func() error {
		var err error
		writer, err = asciicast.NewWriter(out, asciicast.Header{Width: cols, Height: rows, StartMS: startMS, Title: row.AssetName})
		if err != nil {
			return err
		}
		for index, chunk := range held {
			if index == sizeIndex {
				continue // already expressed by the header size
			}
			if err := writeCastChunk(writer, chunk); err != nil {
				return err
			}
		}
		held = nil
		return nil
	}
	afterSeq := int64(0)
	for {
		chunks, err := s.database.TranscriptChunks(ctx, row.ID, afterSeq, transcriptCastPageBytes)
		if err != nil {
			return asciicast.Result{}, err
		}
		if len(chunks) == 0 {
			break
		}
		for _, chunk := range chunks {
			if first {
				first = false
				if chunk.TS > 0 && (startMS <= 0 || chunk.TS < startMS) {
					startMS = chunk.TS
				}
			}
			if writer == nil {
				switch chunk.Kind {
				case store.TranscriptChunkKindResize:
					if c, r, ok := parseCastResize(chunk.Data); ok && cols == 0 {
						cols, rows = c, r
						sizeIndex = len(held)
					}
					held = append(held, chunk)
					continue
				case store.TranscriptChunkKindOutput:
					if err := openWriter(); err != nil {
						return asciicast.Result{}, err
					}
				default:
					continue
				}
			}
			if err := writeCastChunk(writer, chunk); err != nil {
				return asciicast.Result{}, err
			}
		}
		afterSeq = chunks[len(chunks)-1].Seq + 1
		if afterSeq >= row.Chunks {
			break
		}
	}
	if writer == nil {
		if err := openWriter(); err != nil {
			return asciicast.Result{}, err
		}
	}
	return writer.Close()
}

func writeCastChunk(writer *asciicast.Writer, chunk store.TranscriptChunkRow) error {
	switch chunk.Kind {
	case store.TranscriptChunkKindOutput:
		return writer.Output(chunk.TS, chunk.Data)
	case store.TranscriptChunkKindResize:
		if cols, rows, ok := parseCastResize(chunk.Data); ok {
			return writer.Resize(chunk.TS, cols, rows)
		}
	}
	// Keyboard input (and unknown kinds) are never exported: input can carry passwords.
	return nil
}

func parseCastResize(data []byte) (cols, rows int, ok bool) {
	var size struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if json.Unmarshal(data, &size) != nil || size.Cols <= 0 || size.Rows <= 0 {
		return 0, 0, false
	}
	return size.Cols, size.Rows, true
}
