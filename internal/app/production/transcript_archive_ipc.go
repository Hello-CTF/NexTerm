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
	"sync"
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

	// transcriptArchiveManifestVersion 2 exports still-recording transcripts (marked partial) instead of skipping them.
	transcriptArchiveManifestVersion = 2
	maxTranscriptArchiveTaskIDBytes  = 128
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
	// TaskID names the export for transcript_export_archive_progress and transcript_export_archive_cancel.
	TaskID string `json:"taskId,omitempty"`
}

type transcriptArchiveTaskRequest struct {
	TaskID string `json:"taskId"`
}

// transcriptArchiveProgress is the state of a running batch export. Bytes counts the recorded bytes (the size the dry
// run estimates), each transcript counted once whatever the number of files written from it.
type transcriptArchiveProgress struct {
	TaskID string `json:"taskId"`
	// Running is false when no export with this task id is running (not started yet, or finished).
	Running         bool   `json:"running"`
	Transcripts     int64  `json:"transcripts"`
	DoneTranscripts int64  `json:"doneTranscripts"`
	Bytes           int64  `json:"bytes"`
	DoneBytes       int64  `json:"doneBytes"`
	Current         string `json:"current,omitempty"`
}

type transcriptArchiveSkipped struct {
	// ContentOmitted counts transcripts whose content was not synced to this device.
	ContentOmitted int64 `json:"contentOmitted"`
}

type transcriptExportArchiveResult struct {
	Path string `json:"path"`
	// Matched counts the transcripts the filter selected, Exported those written into the archive.
	Matched  int64                    `json:"matched"`
	Exported int64                    `json:"exported"`
	Skipped  transcriptArchiveSkipped `json:"skipped"`
	// Partial counts the exported transcripts that were still recording; their files stop where the export started.
	Partial int64 `json:"partial"`
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
	ID        string `json:"id"`
	AssetID   string `json:"assetId"`
	Host      string `json:"host"`
	StartedAt int64  `json:"startedAt"`
	EndedAt   *int64 `json:"endedAt"`
	Bytes     int64  `json:"bytes"`
	Truncated bool   `json:"truncated"`
	Starred   bool   `json:"starred"`
	// Partial marks a transcript that was still recording: its files stop where the export started.
	Partial bool     `json:"partial"`
	Files   []string `json:"files"`
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

// exportTranscriptArchive writes (or, for a dry run, counts) the archive. progress, when set, receives the running
// totals as the export advances; the last call carries the final counts.
func (s *terminalCommandService) exportTranscriptArchive(ctx context.Context, input transcriptExportArchiveRequest,
	progress func(transcriptArchiveProgress)) (transcriptExportArchiveResult, error) {
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
		if err := s.writeTranscriptArchive(ctx, input, location, plan, counted, filepath.Dir(path), maxBytes, &result, progress); err != nil {
			return err
		}
		result.Bytes = counted.n
		return nil
	})
	if errors.Is(err, base.ErrTransferTooLarge) {
		// Browser mode only: the archive, or a tar.gz entry spooled on the server, passed the account's staging budget.
		return transcriptExportArchiveResult{}, ipc.WrapError(ipc.CodeIO, "压缩包超过服务端允许的暂存大小上限，已停止导出，未保存文件。请缩小时间范围后重试", err).
			WithDetail(map[string]any{"resource": "local_staging", "reason": "too_large"})
	}
	if errors.Is(err, context.Canceled) {
		return transcriptExportArchiveResult{}, ipc.WrapError(ipc.Code("cancelled"), "已取消导出", err)
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
		Version: transcriptArchiveManifestVersion, GeneratedAt: time.Now().UnixMilli(), Content: input.Content, Timestamps: input.Timestamps,
		Filter: transcriptArchiveManifestFilter{
			AssetID: input.AssetID, StartedFrom: input.StartedFrom, StartedBefore: input.StartedBefore, StarredOnly: input.StarredOnly,
		},
		Transcripts: []transcriptArchiveManifestEntry{},
		Skipped:     []transcriptArchiveManifestSkip{},
	}}
	plan.result.Matched = int64(len(rows))
	for _, row := range rows {
		host := transcriptExportHost(row)
		if row.ContentOmitted {
			plan.result.Skipped.ContentOmitted++
			plan.manifest.Skipped = append(plan.manifest.Skipped, transcriptArchiveManifestSkip{
				ID: row.ID, AssetID: row.AssetID, Host: host, StartedAt: row.StartedAt, Reason: transcriptSkipContentOmitted,
			})
			continue
		}
		// Like the single export, a transcript that is still recording is exported up to the point the export lists it.
		partial := s.transcriptActive(row)
		if partial {
			plan.result.Partial++
		}
		entry := transcriptArchiveManifestEntry{
			ID: row.ID, AssetID: row.AssetID, Host: host, StartedAt: row.StartedAt, EndedAt: row.EndedAt,
			Bytes: row.Bytes, Truncated: row.Truncated, Starred: row.Starred, Partial: partial,
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
	plan transcriptArchivePlan, out io.Writer, tempDir string, maxBytes int64, result *transcriptExportArchiveResult,
	progress func(transcriptArchiveProgress)) error {
	state := transcriptArchiveProgress{Running: true, Transcripts: plan.result.Exported, Bytes: plan.result.EstimatedBytes}
	report := func() {
		if progress != nil {
			progress(state)
		}
	}
	report()
	var archive transcriptArchiveWriter
	if input.Format == transcriptArchiveTarGz {
		archive = newTarGzArchive(out, tempDir, maxBytes)
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
		files := plan.manifest.Transcripts[index].Files
		modified := time.UnixMilli(row.StartedAt)
		// Each file reads the whole recording, so a transcript's share of the bytes is what its files read over the
		// number of files, never more than the recorded size.
		doneBefore, read := state.DoneBytes, int64(0)
		onRead := func(n int64) {
			read += n
			state.DoneBytes = doneBefore + min(read/int64(len(files)), row.Bytes)
			report()
		}
		state.Current = strings.TrimSuffix(files[0], filepath.Ext(files[0]))
		report()
		for _, name := range files {
			var add func(io.Writer) error
			if strings.HasSuffix(name, ".cast") {
				add = func(w io.Writer) error {
					summary, err := s.encodeTranscriptCast(ctx, row, w, onRead)
					if summary.TimingEstimated {
						result.TimingEstimated++
					}
					return err
				}
			} else {
				add = func(w io.Writer) error {
					summary, err := s.renderTranscriptText(ctx, row, w, termtext.Options{Timestamps: input.Timestamps, Location: location}, onRead)
					result.FullScreenSegments += summary.FullScreenSegments
					return err
				}
			}
			if err := archive.Add(name, modified, add); err != nil {
				return err
			}
		}
		state.DoneBytes = doneBefore + row.Bytes
		state.DoneTranscripts++
		report()
	}
	if err := archive.Close(); err != nil {
		return err
	}
	state.Current = ""
	report()
	return nil
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
// maxBytes (browser mode's staging budget, zero on desktop) also bounds each spooled entry, which sits on the same disk.
type tarGzArchive struct {
	gzip     *gzip.Writer
	tar      *tar.Writer
	tempDir  string
	maxBytes int64
}

func newTarGzArchive(out io.Writer, tempDir string, maxBytes int64) *tarGzArchive {
	compressed := gzip.NewWriter(out)
	return &tarGzArchive{gzip: compressed, tar: tar.NewWriter(compressed), tempDir: tempDir, maxBytes: maxBytes}
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
	if err := write(fslocal.LimitWriter(spool, a.maxBytes)); err != nil {
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

// ensureTranscriptArchiveTaskID accepts the client-generated ids (UUIDs, ULIDs and similar).
func ensureTranscriptArchiveTaskID(taskID string) error {
	if taskID == "" || len(taskID) > maxTranscriptArchiveTaskIDBytes {
		return ipc.BadParam(errors.New("taskId 无效"))
	}
	for _, r := range taskID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ipc.BadParam(errors.New("taskId 无效"))
		}
	}
	return nil
}

type transcriptArchiveProgressKey struct {
	owner  string
	taskID string
}

// transcriptArchiveProgressRegistry holds the latest progress of each running batch export, per account, for the
// dialog to poll.
type transcriptArchiveProgressRegistry struct {
	mu      sync.Mutex
	entries map[transcriptArchiveProgressKey]transcriptArchiveProgress
	// observe, when set (tests), runs after each update on the exporting goroutine.
	observe func(transcriptArchiveProgress)
}

func newTranscriptArchiveProgressRegistry() *transcriptArchiveProgressRegistry {
	return &transcriptArchiveProgressRegistry{entries: map[transcriptArchiveProgressKey]transcriptArchiveProgress{}}
}

func (r *transcriptArchiveProgressRegistry) set(ctx context.Context, progress transcriptArchiveProgress) {
	r.mu.Lock()
	r.entries[transcriptArchiveProgressKey{owner: ipc.OwnerID(ctx), taskID: progress.TaskID}] = progress
	r.mu.Unlock()
	if r.observe != nil {
		r.observe(progress)
	}
}

func (r *transcriptArchiveProgressRegistry) remove(ctx context.Context, taskID string) {
	r.mu.Lock()
	delete(r.entries, transcriptArchiveProgressKey{owner: ipc.OwnerID(ctx), taskID: taskID})
	r.mu.Unlock()
}

func (r *transcriptArchiveProgressRegistry) get(ctx context.Context, taskID string) transcriptArchiveProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	if progress, ok := r.entries[transcriptArchiveProgressKey{owner: ipc.OwnerID(ctx), taskID: taskID}]; ok {
		return progress
	}
	return transcriptArchiveProgress{TaskID: taskID}
}

// runTranscriptArchive runs an export under its task id, so transcript_export_archive_cancel can stop it (also when
// the cancel arrives first) and transcript_export_archive_progress can report it.
func (s *terminalCommandService) runTranscriptArchive(ctx context.Context, tasks *fsTransferRegistry,
	progress *transcriptArchiveProgressRegistry, input transcriptExportArchiveRequest) (transcriptExportArchiveResult, error) {
	if input.DryRun {
		return s.exportTranscriptArchive(ctx, input, nil)
	}
	if input.TaskID != "" {
		if err := ensureTranscriptArchiveTaskID(input.TaskID); err != nil {
			return transcriptExportArchiveResult{}, err
		}
	}
	exportCtx, taskID, finish, err := tasks.begin(ctx, input.TaskID, "")
	if errors.Is(err, context.Canceled) {
		return transcriptExportArchiveResult{}, ipc.WrapError(ipc.Code("cancelled"), "已取消导出", err)
	}
	if err != nil {
		return transcriptExportArchiveResult{}, err
	}
	defer finish()
	defer progress.remove(ctx, taskID)
	return s.exportTranscriptArchive(exportCtx, input, func(state transcriptArchiveProgress) {
		state.TaskID = taskID
		progress.set(ctx, state)
	})
}
