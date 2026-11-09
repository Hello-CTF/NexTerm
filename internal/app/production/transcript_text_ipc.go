package production

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/termtext"
)

// maxUTCOffsetMinutes bounds a client-reported zone offset (UTC-14:00 to UTC+14:00 cover every real zone).
const maxUTCOffsetMinutes = 14 * 60

type transcriptExportTextRequest struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Timestamps prefixes each line with the time its output arrived.
	Timestamps bool `json:"timestamps,omitempty"`
	// UTCOffsetMinutes is the viewer's zone offset for the timestamps; the backend's local zone is used when it is absent.
	UTCOffsetMinutes *int `json:"utcOffsetMinutes,omitempty"`
}

type transcriptExportTextResult struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
	Lines int64  `json:"lines"`
	// FullScreenSegments counts the full-screen (alternate screen) program runs whose output is not in the text.
	FullScreenSegments int64 `json:"fullScreenSegments"`
}

func (s *terminalCommandService) exportTranscriptText(ctx context.Context, input transcriptExportTextRequest) (transcriptExportTextResult, error) {
	if err := store.EnsureID(input.ID); err != nil {
		return transcriptExportTextResult{}, err
	}
	location := time.Local
	if input.UTCOffsetMinutes != nil {
		offset := *input.UTCOffsetMinutes
		if offset < -maxUTCOffsetMinutes || offset > maxUTCOffsetMinutes {
			return transcriptExportTextResult{}, ipc.BadParam(errors.New("时区偏移超出范围"))
		}
		location = termtext.FixedOffset(offset)
	}
	row, err := s.database.TranscriptGet(ctx, input.ID)
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	if row.ContentOmitted {
		return transcriptExportTextResult{}, ipc.BadParam(errors.New("该录像只同步了元数据，没有可导出的内容"))
	}
	path, err := resolveStagedLocalPath(ctx, s.stagedBlobs, input.Path)
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	var summary termtext.Result
	err = atomicfile.Write(path, 0o600, func(out io.Writer) error {
		var renderErr error
		summary, renderErr = s.renderTranscriptText(ctx, row, out, termtext.Options{Timestamps: input.Timestamps, Location: location})
		return renderErr
	})
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	return transcriptExportTextResult{
		Path: path, Bytes: summary.Bytes, Lines: summary.Lines, FullScreenSegments: summary.FullScreenSegments,
	}, nil
}

// renderTranscriptText streams the transcript pages through the plain-text renderer, one page in memory at a time.
func (s *terminalCommandService) renderTranscriptText(ctx context.Context, row store.TranscriptRow, out io.Writer, opts termtext.Options) (termtext.Result, error) {
	renderer := termtext.NewRenderer(out, opts)
	afterSeq := int64(0)
	for {
		chunks, err := s.database.TranscriptChunks(ctx, row.ID, afterSeq, transcriptCastPageBytes)
		if err != nil {
			return termtext.Result{}, err
		}
		if len(chunks) == 0 {
			break
		}
		for _, chunk := range chunks {
			// Keyboard input (and unknown kinds) are never exported: input can carry passwords.
			switch chunk.Kind {
			case store.TranscriptChunkKindOutput:
				if err := renderer.Write(chunk.TS, chunk.Data); err != nil {
					return termtext.Result{}, err
				}
			case store.TranscriptChunkKindResize:
				if cols, _, ok := parseCastResize(chunk.Data); ok {
					renderer.Resize(cols)
				}
			}
		}
		afterSeq = chunks[len(chunks)-1].Seq + 1
		if afterSeq >= row.Chunks {
			break
		}
	}
	return renderer.Close()
}
