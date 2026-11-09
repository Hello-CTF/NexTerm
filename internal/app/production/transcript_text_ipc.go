package production

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	// The browser reports an IANA zone; server hosts (containers, minimal images) may not ship a zone database.
	_ "time/tzdata"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/termtext"
)

const (
	// maxUTCOffsetMinutes bounds a client-reported zone offset (UTC-14:00 to UTC+14:00 cover every real zone).
	maxUTCOffsetMinutes  = 14 * 60
	maxTimeZoneNameBytes = 64
	// The text may grow past its input (timestamp prefixes on short lines, cursor moves padded with blanks), but not without
	// bound: the output stays within a fixed allowance plus a multiple of the recorded output consumed so far.
	transcriptTextBaseAllowance = 1 << 20
	transcriptTextGrowthFactor  = 32
)

var errTranscriptTextTooLarge = errors.New("transcript text export exceeds its size limit")

// transcriptTextLimit caps the export at base + factor x the input consumed, so crafted output cannot inflate the file.
type transcriptTextLimit struct {
	out     io.Writer
	input   int64
	written int64
}

func (l *transcriptTextLimit) allowance() int64 {
	return transcriptTextBaseAllowance + transcriptTextGrowthFactor*l.input
}

func (l *transcriptTextLimit) Write(p []byte) (int, error) {
	if l.written+int64(len(p)) > l.allowance() {
		return 0, errTranscriptTextTooLarge
	}
	n, err := l.out.Write(p)
	l.written += int64(n)
	return n, err
}

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
	if errors.Is(err, errTranscriptTextTooLarge) {
		return transcriptExportTextResult{}, ipc.BadParam(errors.New("导出的文本远大于录像本身，可能含有异常的控制序列，已停止导出"))
	}
	if err != nil {
		return transcriptExportTextResult{}, err
	}
	return transcriptExportTextResult{
		Path: path, Bytes: summary.Bytes, Lines: summary.Lines, FullScreenSegments: summary.FullScreenSegments,
	}, nil
}

func transcriptTextLocation(input transcriptExportTextRequest) (*time.Location, error) {
	if input.TimeZone != "" && len(input.TimeZone) <= maxTimeZoneNameBytes {
		if location, err := time.LoadLocation(input.TimeZone); err == nil {
			return location, nil
		}
	}
	if input.UTCOffsetMinutes != nil {
		offset := *input.UTCOffsetMinutes
		if offset < -maxUTCOffsetMinutes || offset > maxUTCOffsetMinutes {
			return nil, ipc.BadParam(errors.New("时区偏移超出范围"))
		}
		return termtext.FixedOffset(offset), nil
	}
	if input.TimeZone != "" {
		return nil, ipc.BadParam(fmt.Errorf("无法识别时区：%s", truncateForError(input.TimeZone)))
	}
	return time.Local, nil
}

func truncateForError(value string) string {
	if len(value) > maxTimeZoneNameBytes {
		return value[:maxTimeZoneNameBytes] + "..."
	}
	return value
}

// renderTranscriptText streams the transcript pages through the plain-text renderer, one page in memory at a time.
func (s *terminalCommandService) renderTranscriptText(ctx context.Context, row store.TranscriptRow, out io.Writer, opts termtext.Options) (termtext.Result, error) {
	limit := &transcriptTextLimit{out: out}
	renderer := termtext.NewRenderer(limit, opts)
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
			if err := ctx.Err(); err != nil {
				return termtext.Result{}, err
			}
			// Keyboard input (and unknown kinds) are never exported: input can carry passwords.
			switch chunk.Kind {
			case store.TranscriptChunkKindOutput:
				limit.input += int64(len(chunk.Data))
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
