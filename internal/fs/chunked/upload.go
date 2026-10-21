package chunked

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

const (
	DefaultChunkSize = 1 << 20
	MaxChunkSize     = 4 << 20
	commitBufferSize = 256 << 10
	cleanupTimeout   = 10 * time.Second
)

var (
	ErrChunkConflict = errors.New("chunk conflicts with an already completed chunk")
	errPartMismatch  = errors.New("staged chunk mismatch")
)

type Chunk struct {
	Index  int64
	Size   int64
	SHA256 string
	Data   []byte
}

type Upload struct {
	mu         sync.Mutex
	id         string
	remotePath string
	owner      string
	total      int64
	chunkSize  int64
	chunkCount int64
	received   int64
	// counted tracks the chunks already included in received; confirmed
	// tracks the chunks the current session presented and had verified.
	// Commit requires every chunk confirmed, so staged content from a
	// crashed session can never be committed unverified.
	counted   map[int64]struct{}
	confirmed map[int64]struct{}
	prepared  bool
	committed bool
	aborted   bool
}

// Option customizes an Upload.
type Option func(*Upload)

// WithOwner binds the upload's staging metadata to the given owner identity.
func WithOwner(owner string) Option {
	return func(u *Upload) { u.owner = owner }
}

func New(uploadID, remotePath string, total int64, chunkSize int, options ...Option) (*Upload, error) {
	if !ids.Valid(uploadID) {
		return nil, fmt.Errorf("invalid upload id")
	}
	if remotePath == "" || strings.ContainsRune(remotePath, 0) {
		return nil, fmt.Errorf("remote path is empty or contains NUL")
	}
	if total < 0 {
		return nil, fmt.Errorf("total size must not be negative")
	}
	if chunkSize == 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize <= 0 || chunkSize > MaxChunkSize {
		return nil, fmt.Errorf("chunk size must be between 1 and %d bytes", MaxChunkSize)
	}
	count := total / int64(chunkSize)
	if total%int64(chunkSize) != 0 {
		count++
	}
	upload := &Upload{
		id: uploadID, remotePath: remotePath, total: total, chunkSize: int64(chunkSize),
		chunkCount: count, counted: map[int64]struct{}{}, confirmed: map[int64]struct{}{},
	}
	for _, option := range options {
		option(upload)
	}
	if len(upload.owner) > stagingParamsMaxOwner {
		return nil, fmt.Errorf("upload owner length %d exceeds the %d-byte staging metadata limit", len(upload.owner), stagingParamsMaxOwner)
	}
	return upload, nil
}

func (u *Upload) Total() int64 {
	return u.total
}

// ID returns the upload's staging ID.
func (u *Upload) ID() string {
	return u.id
}

// MatchesStaged reports whether a staged group was left behind by an upload
// with exactly this upload's owner, parameters, and intact part sizes.
// Version 2 metadata binds the group to its owner; legacy 16-byte metadata
// carries no owner and only matches the desktop's empty owner, so a
// multi-account begin never adopts it. Groups without readable params never
// match: they predate crash-safe resume or lost a write race, and are left
// for the reaper rather than risking a mixed adoption.
func (u *Upload) MatchesStaged(staged StagedUpload) bool {
	if !staged.HasParams || staged.Total != u.total || staged.ChunkSize != u.chunkSize {
		return false
	}
	switch staged.ParamsVersion {
	case stagingParamsVersion2:
		if staged.Owner != u.owner {
			return false
		}
	case stagingParamsVersion1:
		if u.owner != "" {
			return false
		}
	default:
		return false
	}
	for index, size := range staged.Parts {
		if index < 0 || index >= u.chunkCount || size != u.expectedChunkSize(index)+sha256.Size {
			return false
		}
	}
	return true
}

func (u *Upload) WriteChunk(ctx context.Context, destination base.FileSystem, chunk Chunk) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return u.received, err
	}
	if u.committed || u.aborted {
		return u.received, fmt.Errorf("upload session is closed")
	}
	if err := u.validateChunk(chunk); err != nil {
		return u.received, err
	}
	digest := sha256.Sum256(chunk.Data)
	actualSHA256 := hex.EncodeToString(digest[:])
	if !strings.EqualFold(actualSHA256, chunk.SHA256) {
		return u.received, fmt.Errorf("chunk %d checksum mismatch", chunk.Index)
	}
	path := u.partPath(chunk.Index)
	_, err := destination.Size(ctx, path)
	switch {
	case err == nil:
		if err := readPart(ctx, destination, path, chunk.Size, actualSHA256, nil, make([]byte, commitBufferSize)); err != nil {
			if errors.Is(err, errPartMismatch) {
				return u.received, fmt.Errorf("%w: index %d", ErrChunkConflict, chunk.Index)
			}
			return u.received, err
		}
		u.confirmChunk(chunk.Index, chunk.Size)
		return u.received, nil
	case !errors.Is(err, fs.ErrNotExist):
		return u.received, err
	}
	if err := u.prepare(ctx, destination); err != nil {
		return u.received, err
	}
	if err := writePart(ctx, destination, path, chunk.Data, digest); err != nil {
		cleanupPath(path, destination.Delete)
		u.cleanupParamsAfterFailedFirstChunk(destination)
		return u.received, err
	}
	if err := ctx.Err(); err != nil {
		cleanupPath(path, destination.Delete)
		u.cleanupParamsAfterFailedFirstChunk(destination)
		return u.received, err
	}
	if err := appendManifest(ctx, destination, u.manifestPath(), chunk.Index); err != nil {
		cleanupPath(path, destination.Delete)
		u.cleanupParamsAfterFailedFirstChunk(destination)
		return u.received, err
	}
	u.confirmChunk(chunk.Index, chunk.Size)
	return u.received, nil
}

// confirmChunk marks a chunk as presented and verified by the current
// session, counting it toward progress exactly once.
func (u *Upload) confirmChunk(index, size int64) {
	if _, ok := u.counted[index]; !ok {
		u.counted[index] = struct{}{}
		u.received += size
	}
	u.confirmed[index] = struct{}{}
}

func (u *Upload) validateChunk(chunk Chunk) error {
	if chunk.Index < 0 || chunk.Index >= u.chunkCount {
		return fmt.Errorf("chunk index %d out of range", chunk.Index)
	}
	expected := u.expectedChunkSize(chunk.Index)
	if chunk.Size != expected || int64(len(chunk.Data)) != expected {
		return fmt.Errorf("chunk %d size mismatch: got %d/%d bytes, want %d", chunk.Index, chunk.Size, len(chunk.Data), expected)
	}
	if len(chunk.SHA256) != sha256.Size*2 {
		return fmt.Errorf("chunk %d SHA-256 is invalid", chunk.Index)
	}
	if _, err := hex.DecodeString(chunk.SHA256); err != nil {
		return fmt.Errorf("chunk %d SHA-256 is invalid: %w", chunk.Index, err)
	}
	return nil
}

func (u *Upload) expectedChunkSize(index int64) int64 {
	remaining := u.total - index*u.chunkSize
	if remaining < u.chunkSize {
		return remaining
	}
	return u.chunkSize
}

func (u *Upload) Commit(ctx context.Context, destination base.FileSystem, activity ...func()) (int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return u.received, err
	}
	if u.committed {
		return u.total, nil
	}
	if u.aborted {
		return u.received, fmt.Errorf("upload session is closed")
	}
	if u.received != u.total {
		return u.received, fmt.Errorf("upload is incomplete: %d of %d bytes", u.received, u.total)
	}
	if len(u.confirmed) != int(u.chunkCount) {
		return u.received, fmt.Errorf("upload has %d unconfirmed chunks: re-send every chunk so staged content is verified before commit", u.chunkCount-int64(len(u.confirmed)))
	}
	if err := u.prepare(ctx, destination); err != nil {
		return u.received, err
	}
	temporary := u.commitPath()
	if err := u.assemble(ctx, destination, temporary, activity...); err != nil {
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	mode, err := TargetMode(ctx, destination, u.remotePath)
	if err != nil {
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	if err := destination.Chmod(ctx, temporary, mode); err != nil && !errors.Is(err, base.ErrUnsupported) {
		// Mode preservation is best-effort: a filesystem without chmod
		// capability (the Windows local FS) still commits verified content
		// atomically; any other chmod failure stays fatal.
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	for index := int64(0); index < u.chunkCount; index++ {
		if err := destination.Delete(ctx, u.partPath(index), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			cleanupPath(temporary, destination.Delete)
			return u.received, fmt.Errorf("remove staged chunk %d: %w", index, err)
		}
	}
	if err := destination.Delete(ctx, u.manifestPath(), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	if err := destination.Delete(ctx, u.paramsPath(), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	if err := ctx.Err(); err != nil {
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	if err := AtomicRename(ctx, destination, temporary, u.remotePath); err != nil {
		cleanupPath(temporary, destination.Delete)
		return u.received, err
	}
	u.committed = true
	return u.total, nil
}

func (u *Upload) assemble(ctx context.Context, destination base.FileSystem, temporary string, activity ...func()) (err error) {
	target, err := destination.OpenWrite(ctx, temporary, false)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = target.Close()
		}
	}()
	buffer := make([]byte, commitBufferSize)
	for index := int64(0); index < u.chunkCount; index++ {
		if err := u.appendPart(ctx, destination, target, index, buffer); err != nil {
			return err
		}
		if len(activity) > 0 && activity[0] != nil {
			activity[0]()
		}
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	return nil
}

func (u *Upload) appendPart(ctx context.Context, destination base.FileSystem, target io.Writer, index int64, buffer []byte) error {
	if err := readPart(ctx, destination, u.partPath(index), u.expectedChunkSize(index), "", target, buffer); err != nil {
		return fmt.Errorf("staged chunk %d: %w", index, err)
	}
	return nil
}

func readPart(ctx context.Context, destination base.FileSystem, path string, dataSize int64, expectedSHA256 string, target io.Writer, buffer []byte) (err error) {
	source, err := destination.OpenRead(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := source.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if source.Size() != dataSize+sha256.Size {
		return fmt.Errorf("%w: size is %d, want %d", errPartMismatch, source.Size(), dataSize+sha256.Size)
	}
	var header [sha256.Size]byte
	if _, err := io.ReadFull(source, header[:]); err != nil {
		return fmt.Errorf("%w: read header: %v", errPartMismatch, err)
	}
	storedSHA256 := hex.EncodeToString(header[:])
	if expectedSHA256 == "" {
		expectedSHA256 = storedSHA256
	} else if !strings.EqualFold(expectedSHA256, storedSHA256) {
		return fmt.Errorf("%w: header checksum changed", errPartMismatch)
	}
	digest := sha256.New()
	remaining := dataSize
	for remaining > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		chunk := buffer
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, readErr := source.Read(chunk)
		if n < 0 || n > len(chunk) {
			return fmt.Errorf("invalid read count %d", n)
		}
		if n > 0 {
			if target != nil {
				if _, err := writeAll(ctx, target, chunk[:n]); err != nil {
					return err
				}
			}
			_, _ = digest.Write(chunk[:n])
			remaining -= int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			return fmt.Errorf("%w: %v", errPartMismatch, io.ErrUnexpectedEOF)
		}
		if readErr != nil {
			return readErr
		}
		if n == 0 {
			return io.ErrNoProgress
		}
	}
	var extra [1]byte
	n, readErr := source.Read(extra[:])
	if n > 0 {
		return fmt.Errorf("%w: content grew", errPartMismatch)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); !strings.EqualFold(actual, expectedSHA256) {
		return fmt.Errorf("%w: content checksum mismatch", errPartMismatch)
	}
	return ctx.Err()
}

func (u *Upload) Abort(ctx context.Context, destination base.FileSystem) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.committed {
		return nil
	}
	u.aborted = true
	var errs []error
	manifest, err := destination.OpenRead(ctx, u.manifestPath())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if err == nil {
		if manifest.Size() < 0 {
			errs = append(errs, fmt.Errorf("invalid upload manifest size %d", manifest.Size()))
		} else {
			var record [8]byte
			for {
				if err := ctx.Err(); err != nil {
					errs = append(errs, err)
					break
				}
				_, readErr := io.ReadFull(manifest, record[:])
				if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					break
				}
				if readErr != nil {
					errs = append(errs, readErr)
					break
				}
				index := int64(binary.LittleEndian.Uint64(record[:]))
				if index < 0 || index >= u.chunkCount {
					errs = append(errs, fmt.Errorf("invalid upload manifest chunk index %d", index))
					continue
				}
				if err := destination.Delete(ctx, u.partPath(index), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
			}
		}
		if closeErr := manifest.Close(); closeErr != nil {
			errs = append(errs, closeErr)
		}
	}
	if len(errs) == 0 {
		if err := destination.Delete(ctx, u.manifestPath(), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	if err := destination.Delete(ctx, u.paramsPath(), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	if err := destination.Delete(ctx, u.commitPath(), false); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (u *Upload) prepare(ctx context.Context, destination base.FileSystem) error {
	if u.prepared {
		return ctx.Err()
	}
	if err := ensureRemoteDir(ctx, destination, parentDir(u.remotePath)); err != nil {
		return err
	}
	if err := u.writeStagingParams(ctx, destination); err != nil {
		cleanupPath(u.paramsPath(), destination.Delete)
		return err
	}
	u.prepared = true
	return nil
}

// cleanupParamsAfterFailedFirstChunk removes the params file written by
// prepare when the first staged chunk never landed, so a canceled upload
// leaves no staging artifacts behind. The next attempt rewrites it.
func (u *Upload) cleanupParamsAfterFailedFirstChunk(destination base.FileSystem) {
	if len(u.counted) != 0 {
		return
	}
	cleanupPath(u.paramsPath(), destination.Delete)
	u.prepared = false
}

func StagingPath(remotePath, uploadID, kind string) string {
	index := strings.LastIndexAny(remotePath, `/\`)
	return remotePath[:index+1] + "." + remotePath[index+1:] + ".nexterm-" + uploadID + "." + kind
}

func (u *Upload) partPath(index int64) string {
	return StagingPath(u.remotePath, u.id, fmt.Sprintf("part-%012d", index))
}

func (u *Upload) manifestPath() string {
	return StagingPath(u.remotePath, u.id, "manifest")
}

func (u *Upload) paramsPath() string {
	return StagingPath(u.remotePath, u.id, "params")
}

func (u *Upload) commitPath() string {
	return StagingPath(u.remotePath, u.id, "commit")
}

func writePart(ctx context.Context, destination base.FileSystem, path string, data []byte, digest [sha256.Size]byte) (err error) {
	writer, err := destination.OpenWrite(ctx, path, false)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
		}
	}()
	if _, err := writeAll(ctx, writer, digest[:]); err != nil {
		return err
	}
	if _, err := writeAll(ctx, writer, data); err != nil {
		return err
	}
	if err := writer.Sync(); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	return nil
}

func appendManifest(ctx context.Context, destination base.FileSystem, path string, index int64) (err error) {
	writer, err := destination.OpenWrite(ctx, path, true)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
		}
	}()
	var record [8]byte
	binary.LittleEndian.PutUint64(record[:], uint64(index))
	if _, err := writeAll(ctx, writer, record[:]); err != nil {
		return err
	}
	if err := writer.Sync(); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		closed = true
		return err
	}
	closed = true
	return nil
}

func writeAll(ctx context.Context, writer io.Writer, data []byte) (int64, error) {
	var written int64
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		n, err := writer.Write(data)
		if n < 0 || n > len(data) {
			return written, fmt.Errorf("invalid write count %d", n)
		}
		if n > 0 {
			written += int64(n)
			data = data[n:]
		}
		if err != nil {
			return written, err
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

type AtomicRenamer interface {
	AtomicRename(context.Context, string, string) error
}

func AtomicRename(ctx context.Context, destination base.FileSystem, from, to string) error {
	if renamer, ok := destination.(AtomicRenamer); ok {
		return renamer.AtomicRename(ctx, from, to)
	}
	_, err := destination.Size(ctx, to)
	if errors.Is(err, fs.ErrNotExist) {
		return destination.Rename(ctx, from, to)
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: filesystem cannot atomically replace an existing upload target", base.ErrUnsupported)
}

const DefaultUploadMode fs.FileMode = 0o600

type fileModeProvider interface {
	FileMode(context.Context, string) (fs.FileMode, error)
}

func TargetMode(ctx context.Context, destination base.FileSystem, remotePath string) (fs.FileMode, error) {
	if provider, ok := destination.(fileModeProvider); ok {
		mode, err := provider.FileMode(ctx, remotePath)
		if errors.Is(err, fs.ErrNotExist) {
			return DefaultUploadMode, nil
		}
		return mode.Perm(), err
	}
	parent := parentDir(remotePath)
	if parent == "" {
		parent = "."
	}
	entries, err := destination.List(ctx, parent)
	if err != nil {
		return 0, err
	}
	name := remotePath[strings.LastIndexAny(remotePath, `/\`)+1:]
	for _, entry := range entries {
		if entry.Name == name {
			return entry.Mode.Perm(), nil
		}
	}
	return DefaultUploadMode, nil
}

func cleanupPath(path string, remove func(context.Context, string, bool) error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_ = remove(ctx, path, false)
}

func ensureRemoteDir(ctx context.Context, destination base.FileSystem, dir string) error {
	if dir == "" {
		return nil
	}
	trimmed := strings.TrimRight(dir, `/\`)
	if trimmed == "" {
		return nil
	}
	var missing []string
	for current := trimmed; current != ""; current = parentDir(current) {
		exists, err := destination.Exists(ctx, current)
		if err != nil {
			return err
		}
		if exists {
			break
		}
		missing = append(missing, current)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		if err := destination.Mkdir(ctx, missing[index]); err != nil {
			if exists, statErr := destination.Exists(ctx, missing[index]); statErr == nil && exists {
				continue
			}
			return err
		}
	}
	return nil
}

func parentDir(remotePath string) string {
	trimmed := strings.TrimRight(remotePath, `/\`)
	if trimmed == "" {
		return ""
	}
	index := strings.LastIndexAny(trimmed, `/\`)
	if index < 0 {
		return ""
	}
	if index == 0 || trimmed[index-1] == ':' {
		return trimmed[:index+1]
	}
	return trimmed[:index]
}
