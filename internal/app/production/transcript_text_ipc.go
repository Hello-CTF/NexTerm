package production

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/termtext"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	// maxUTCOffsetMinutes bounds a client-reported zone offset (UTC-14:00 to UTC+14:00 cover every real zone).
	maxUTCOffsetMinutes  = 14 * 60
	maxTimeZoneNameBytes = 64
)

type transcriptExportTextRequest struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	// Timestamps prefixes each line with the time its output arrived.
	Timestamps bool `json:"timestamps,omitempty"`
	// TimeZone is the viewer's IANA zone for the timestamps (browser mode); UTCOffsetMinutes is the fallback when the zone
	// is unknown here. Without either the backend's local zone is used, which is the viewer's zone on desktop.
	TimeZone         string `json:"timeZone,omitempty"`
	UTCOffsetMinutes *int   `json:"utcOffsetMinutes,omitempty"`
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
	location, err := transcriptTextLocation(input)
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	row, err := s.database.TranscriptGet(ctx, input.ID)
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	if row.ContentOmitted {
		return transcriptExportTextResult{}, ipc.BadParam(errors.New("该录像只同步了元数据，没有可导出的内容"))
	}
	path, maxBytes, release, err := resolveStagedWriteTarget(ctx, s.stagedBlobs, input.Path)
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	defer release()
	var summary termtext.Result
	err = atomicfile.Write(path, 0o600, func(out io.Writer) error {
		var renderErr error
		summary, renderErr = s.renderTranscriptText(ctx, row, fslocal.LimitWriter(out, maxBytes), termtext.Options{Timestamps: input.Timestamps, Location: location}, nil)
		return renderErr
	})
	if errors.Is(err, base.ErrTransferTooLarge) {
		return transcriptExportTextResult{}, fsIPCError("", err)
	}
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	return transcriptExportTextResult{
		Path: path, Bytes: summary.Bytes, Lines: summary.Lines, FullScreenSegments: summary.FullScreenSegments,
	}, nil
}

// transcriptTextLocation resolves the viewer's zone. Only browser mode sends one; the server binary embeds the zone database
// (cmd/nexterm-server), and a zone this build cannot load falls back to the reported offset.
func transcriptTextLocation(input transcriptExportTextRequest) (*time.Location, error) {
	return viewerLocation(input.TimeZone, input.UTCOffsetMinutes)
}

func viewerLocation(timeZone string, utcOffsetMinutes *int) (*time.Location, error) {
	if timeZone != "" && len(timeZone) <= maxTimeZoneNameBytes {
		if location, err := time.LoadLocation(timeZone); err == nil {
			return location, nil
		}
	}
	if utcOffsetMinutes != nil {
		offset := *utcOffsetMinutes
		if offset < -maxUTCOffsetMinutes || offset > maxUTCOffsetMinutes {
			return nil, ipc.BadParam(errors.New("时区偏移超出范围"))
		}
		return termtext.FixedOffset(offset), nil
	}
	if timeZone != "" {
		return nil, ipc.BadParam(fmt.Errorf("无法识别时区：%s", truncateForError(timeZone)))
	}
	return time.Local, nil
}

func truncateForError(value string) string {
	if len(value) > maxTimeZoneNameBytes {
		return value[:maxTimeZoneNameBytes] + "..."
	}
	return value
}

// renderTranscriptText streams the transcript pages through the plain-text renderer, one page in memory at a time. The
// renderer keeps cursor moves on one row of at most 1024 columns, so the text grows at most linearly with the recording
// and is written in full; only the caller's write budget (browser mode) bounds it. read, when set, receives the recorded
// bytes of each page as it is consumed.
func (s *terminalCommandService) renderTranscriptText(ctx context.Context, row store.TranscriptRow, out io.Writer, opts termtext.Options, read func(int64)) (termtext.Result, error) {
	renderer := termtext.NewRenderer(out, opts)
	afterSeq := int64(0)
	for {
		chunks, err := s.database.TranscriptChunks(ctx, row.ID, afterSeq, transcriptCastPageBytes)
		if err != nil {
			return termtext.Result{}, err
		}
		chunks = transcriptSnapshotPage(row, chunks)
		if len(chunks) == 0 {
			break
		}
		for _, chunk := range chunks {
			if err := ctx.Err(); err != nil {
				return termtext.Result{}, err
			}
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
		if read != nil {
			read(transcriptPageBytes(chunks))
		}
		afterSeq = chunks[len(chunks)-1].Seq + 1
		if afterSeq >= row.Chunks {
			break
		}
	}
	return renderer.Close()
}
