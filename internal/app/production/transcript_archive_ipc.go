package production

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/atomicfile"
	fslocal "github.com/Hello-CTF/NexTerm/internal/fs/local"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/termtext"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	transcriptArchiveZip   = "zip"
	transcriptArchiveTarGz = "tar.gz"

	transcriptArchiveContentCast = "cast"
	transcriptArchiveContentText = "text"
	transcriptArchiveContentBoth = "both"

	transcriptArchiveManifestName = "manifest.json"

	transcriptSkipContentOmitted = "contentOmitted"
	transcriptSkipActive         = "active"
)

type transcriptExportArchiveRequest struct {
	// Path is the archive to write; it is not needed for a dry run.
	Path string `json:"path,omitempty"`
	// Format is "zip" (default) or "tar.gz".
	Format string `json:"format,omitempty"`
	// AssetID limits the export to one host; empty exports every host.
	AssetID string `json:"assetId,omitempty"`
	// StartedFrom (inclusive) and StartedBefore (exclusive) bound the transcript start time in Unix ms; 0 is unbounded.
	StartedFrom   int64 `json:"startedFrom,omitempty"`
	StartedBefore int64 `json:"startedBefore,omitempty"`
	StarredOnly   bool  `json:"starredOnly,omitempty"`
	// Content is "cast" (default), "text" or "both".
	Content string `json:"content,omitempty"`
	// Timestamps prefixes each text line with the time its output arrived.
	Timestamps bool `json:"timestamps,omitempty"`
	// TimeZone and UTCOffsetMinutes are the viewer's zone, used for the entry names and text timestamps (see the text export).
	TimeZone         string `json:"timeZone,omitempty"`
	UTCOffsetMinutes *int   `json:"utcOffsetMinutes,omitempty"`
	// DryRun only counts what an export with this filter would write.
	DryRun bool `json:"dryRun,omitempty"`
}

type transcriptArchiveSkipped struct {
	// ContentOmitted counts transcripts whose content was not synced to this device.
	ContentOmitted int64 `json:"contentOmitted"`
	// Active counts transcripts that are still being recorded.
	Active int64 `json:"active"`
}

type transcriptExportArchiveResult struct {
	Path string `json:"path"`
	// Matched counts the transcripts the filter selected, Exported those written into the archive.
	Matched  int64                    `json:"matched"`
	Exported int64                    `json:"exported"`
	Skipped  transcriptArchiveSkipped `json:"skipped"`
	// Files counts the transcript files in the archive, without the manifest.
	Files int64 `json:"files"`
	// Bytes is the archive size; EstimatedBytes the recorded size of the transcripts to export, before conversion.
	Bytes          int64 `json:"bytes"`
	EstimatedBytes int64 `json:"estimatedBytes"`
	// FullScreenSegments sums the full-screen program runs left out of the text files.
	FullScreenSegments int64 `json:"fullScreenSegments"`
	// TimingEstimated counts the cast files whose missing timestamps were estimated.
	TimingEstimated int64 `json:"timingEstimated"`
}

type transcriptArchiveManifestEntry struct {
	ID        string   `json:"id"`
	AssetID   string   `json:"assetId"`
	Host      string   `json:"host"`
	StartedAt int64    `json:"startedAt"`
	EndedAt   *int64   `json:"endedAt"`
	Bytes     int64    `json:"bytes"`
	Truncated bool     `json:"truncated"`
	Starred   bool     `json:"starred"`
	Files     []string `json:"files"`
}

type transcriptArchiveManifestSkip struct {
	ID        string `json:"id"`
	AssetID   string `json:"assetId"`
	Host      string `json:"host"`
	StartedAt int64  `json:"startedAt"`
	Reason    string `json:"reason"`
}

type transcriptArchiveManifest struct {
	Version     int                              `json:"version"`
	GeneratedAt int64                            `json:"generatedAt"`
	Content     string                           `json:"content"`
	Timestamps  bool                             `json:"timestamps"`
	Filter      transcriptArchiveManifestFilter  `json:"filter"`
	Transcripts []transcriptArchiveManifestEntry `json:"transcripts"`
	Skipped     []transcriptArchiveManifestSkip  `json:"skipped"`
}

type transcriptArchiveManifestFilter struct {
	AssetID       string `json:"assetId,omitempty"`
	StartedFrom   int64  `json:"startedFrom,omitempty"`
	StartedBefore int64  `json:"startedBefore,omitempty"`
	StarredOnly   bool   `json:"starredOnly"`
}

type transcriptArchivePlan struct {
	rows     []store.TranscriptRow
	manifest transcriptArchiveManifest
	result   transcriptExportArchiveResult
}

func (s *terminalCommandService) exportTranscriptArchive(ctx context.Context, input transcriptExportArchiveRequest) (transcriptExportArchiveResult, error) {
	if input.Format == "" {
		input.Format = transcriptArchiveZip
	}
	if input.Content == "" {
		input.Content = transcriptArchiveContentCast
	}
	if input.Format != transcriptArchiveZip && input.Format != transcriptArchiveTarGz {
		return transcriptExportArchiveResult{}, ipc.BadParam(fmt.Errorf("不支持的压缩格式：%s", truncateForError(input.Format)))
	}
	switch input.Content {
	case transcriptArchiveContentCast, transcriptArchiveContentText, transcriptArchiveContentBoth:
	default:
		return transcriptExportArchiveResult{}, ipc.BadParam(fmt.Errorf("不支持的导出内容：%s", truncateForError(input.Content)))
	}
	if input.AssetID != "" {
		if err := ensureTranscriptAssetID(input.AssetID); err != nil {
			return transcriptExportArchiveResult{}, err
		}
	}
	if input.StartedFrom < 0 || input.StartedBefore < 0 || (input.StartedBefore > 0 && input.StartedBefore <= input.StartedFrom) {
		return transcriptExportArchiveResult{}, ipc.BadParam(errors.New("时间范围无效"))
	}
	location, err := viewerLocation(input.TimeZone, input.UTCOffsetMinutes)
	if err != nil {
		return transcriptExportArchiveResult{}, err
	}
	plan, err := s.planTranscriptArchive(ctx, input, location)
	if err != nil {
		return transcriptExportArchiveResult{}, err
	}
	if input.DryRun {
		return plan.result, nil
	}
	if plan.result.Exported == 0 {
		return plan.result, ipc.BadParam(errors.New("所选范围内没有可导出的记录"))
	}
	path, maxBytes, release, err := resolveStagedWriteTarget(ctx, s.stagedBlobs, input.Path)
	if err != nil {
		return transcriptExportArchiveResult{}, err
	}
	defer release()
	result := plan.result
	err = atomicfile.Write(path, 0o600, func(out io.Writer) error {
		counted := &countingWriter{out: fslocal.LimitWriter(out, maxBytes)}
		if err := s.writeTranscriptArchive(ctx, input, location, plan, counted, filepath.Dir(path), &result); err != nil {
			return err
		}
		result.Bytes = counted.n
		return nil
	})
	if errors.Is(err, base.ErrTransferTooLarge) {
		return transcriptExportArchiveResult{}, fsIPCError("", err)
	}
	if err != nil {
		return transcriptExportArchiveResult{}, err
	}
	result.Path = path
	return result, nil
}

// planTranscriptArchive lists the matching transcripts (metadata only) and decides each one's files, so a dry run and
// the export count the same way and the manifest can lead the archive.
func (s *terminalCommandService) planTranscriptArchive(ctx context.Context, input transcriptExportArchiveRequest, location *time.Location) (transcriptArchivePlan, error) {
	rows, err := s.database.TranscriptListForExport(ctx, store.TranscriptExportFilter{
		AssetID: input.AssetID, StartedFrom: input.StartedFrom, StartedBefore: input.StartedBefore, StarredOnly: input.StarredOnly,
	})
	if err != nil {
		return transcriptArchivePlan{}, err
	}
	plan := transcriptArchivePlan{manifest: transcriptArchiveManifest{
		Version: 1, GeneratedAt: time.Now().UnixMilli(), Content: input.Content, Timestamps: input.Timestamps,
		Filter: transcriptArchiveManifestFilter{
			AssetID: input.AssetID, StartedFrom: input.StartedFrom, StartedBefore: input.StartedBefore, StarredOnly: input.StarredOnly,
		},
		Transcripts: []transcriptArchiveManifestEntry{},
		Skipped:     []transcriptArchiveManifestSkip{},
	}}
	plan.result.Matched = int64(len(rows))
	for _, row := range rows {
		host := transcriptExportHost(row)
		reason := ""
		switch {
		case row.ContentOmitted:
			reason = transcriptSkipContentOmitted
			plan.result.Skipped.ContentOmitted++
		case s.transcriptActive(row):
			reason = transcriptSkipActive
			plan.result.Skipped.Active++
		}
		if reason != "" {
			plan.manifest.Skipped = append(plan.manifest.Skipped, transcriptArchiveManifestSkip{
				ID: row.ID, AssetID: row.AssetID, Host: host, StartedAt: row.StartedAt, Reason: reason,
			})
			continue
		}
		entry := transcriptArchiveManifestEntry{
			ID: row.ID, AssetID: row.AssetID, Host: host, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			Bytes: row.Bytes, Truncated: row.Truncated, Starred: row.Starred,
		}
		dir := transcriptArchiveDir(host)
		if input.Content != transcriptArchiveContentText {
			entry.Files = append(entry.Files, dir+"/"+transcriptExportFileName(row, location, "cast"))
		}
		if input.Content != transcriptArchiveContentCast {
			entry.Files = append(entry.Files, dir+"/"+transcriptExportFileName(row, location, "txt"))
		}
		plan.rows = append(plan.rows, row)
		plan.manifest.Transcripts = append(plan.manifest.Transcripts, entry)
		plan.result.Exported++
		plan.result.Files += int64(len(entry.Files))
		plan.result.EstimatedBytes += row.Bytes
	}
	return plan, nil
}

func (s *terminalCommandService) transcriptActive(row store.TranscriptRow) bool {
	if row.EndedAt != nil {
		return false
	}
	_, err := s.sessions.Session(row.SessionID)
	return err == nil
}

func (s *terminalCommandService) writeTranscriptArchive(ctx context.Context, input transcriptExportArchiveRequest, location *time.Location,
	plan transcriptArchivePlan, out io.Writer, tempDir string, result *transcriptExportArchiveResult) error {
	var archive transcriptArchiveWriter
	if input.Format == transcriptArchiveTarGz {
		archive = newTarGzArchive(out, tempDir)
	} else {
		archive = &zipArchive{writer: zip.NewWriter(out)}
	}
	manifest, err := json.MarshalIndent(plan.manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := archive.Add(transcriptArchiveManifestName, time.UnixMilli(plan.manifest.GeneratedAt), func(w io.Writer) error {
		_, err := w.Write(append(manifest, '\n'))
		return err
	}); err != nil {
		return err
	}
	for index, row := range plan.rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		modified := time.UnixMilli(row.StartedAt)
		for _, name := range plan.manifest.Transcripts[index].Files {
			var add func(io.Writer) error
			if strings.HasSuffix(name, ".cast") {
				add = func(w io.Writer) error {
					summary, err := s.encodeTranscriptCast(ctx, row, w)
					if summary.TimingEstimated {
						result.TimingEstimated++
					}
					return err
				}
			} else {
				add = func(w io.Writer) error {
					summary, err := s.renderTranscriptText(ctx, row, w, termtext.Options{Timestamps: input.Timestamps, Location: location})
					if errors.Is(err, errTranscriptTextTooLarge) {
						return ipc.BadParam(fmt.Errorf("记录 %s 导出的文本远大于录像本身，可能含有异常的控制序列，已停止导出", name))
					}
					result.FullScreenSegments += summary.FullScreenSegments
					return err
				}
			}
			if err := archive.Add(name, modified, add); err != nil {
				return err
			}
		}
	}
	return archive.Close()
}

type transcriptArchiveWriter interface {
	Add(name string, modified time.Time, write func(io.Writer) error) error
	Close() error
}

type zipArchive struct {
	writer *zip.Writer
}

func (a *zipArchive) Add(name string, modified time.Time, write func(io.Writer) error) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified}
	header.SetMode(0o644)
	entry, err := a.writer.CreateHeader(header)
	if err != nil {
		return err
	}
	return write(entry)
}

func (a *zipArchive) Close() error { return a.writer.Close() }

// tarGzArchive spools each entry to a temporary file next to the archive, since a tar header carries the entry size.
type tarGzArchive struct {
	gzip    *gzip.Writer
	tar     *tar.Writer
	tempDir string
}

func newTarGzArchive(out io.Writer, tempDir string) *tarGzArchive {
	compressed := gzip.NewWriter(out)
	return &tarGzArchive{gzip: compressed, tar: tar.NewWriter(compressed), tempDir: tempDir}
}

func (a *tarGzArchive) Add(name string, modified time.Time, write func(io.Writer) error) error {
	spool, err := os.CreateTemp(a.tempDir, ".nexterm-archive-entry-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = spool.Close()
		_ = os.Remove(spool.Name())
	}()
	if err := write(spool); err != nil {
		return err
	}
	size, err := spool.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := a.tar.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: size, ModTime: modified,
	}); err != nil {
		return err
	}
	_, err = io.Copy(a.tar, spool)
	return err
}

func (a *tarGzArchive) Close() error {
	if err := a.tar.Close(); err != nil {
		return err
	}
	return a.gzip.Close()
}

type countingWriter struct {
	out io.Writer
	n   int64
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.out.Write(p)
	w.n += int64(n)
	return n, err
}

func transcriptExportHost(row store.TranscriptRow) string {
	if row.AssetName != "" {
		return row.AssetName
	}
	return "transcript"
}

// transcriptExportFileName is <host>-<YYYY-MM-DD-HHmmss>-<last 6 id chars>.<ext> in the viewer's zone; the frontend's
// single-transcript export builds the same name.
func transcriptExportFileName(row store.TranscriptRow, location *time.Location, extension string) string {
	suffix := row.ID
	if len(suffix) > 6 {
		suffix = suffix[len(suffix)-6:]
	}
	started := time.UnixMilli(row.StartedAt).In(location).Format("2006-01-02-150405")
	return sanitizeExportName(fmt.Sprintf("%s-%s-%s.%s", transcriptExportHost(row), started, suffix, extension))
}

// transcriptArchiveDir is the host's directory inside the archive; it never resolves to "." or "..".
func transcriptArchiveDir(host string) string {
	dir := strings.TrimRight(sanitizeExportName(host), " .")
	if dir == "" {
		return "_"
	}
	return dir
}

func sanitizeExportName(name string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\/:*?"<>|`, r) {
			return '_'
		}
		return r
	}, name)
}
