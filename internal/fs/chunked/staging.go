package chunked

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// stagingParamsSize is the on-disk size of a staging params file: the upload
// total and chunk size as little-endian int64 values.
const stagingParamsSize = 16

// StagedUpload describes one chunked-upload staging group discovered on a
// destination: the staged parts with their sizes, plus which control files
// (params, manifest, commit temporary) survived the crash.
type StagedUpload struct {
	UploadID    string
	Parts       map[int64]int64
	HasManifest bool
	HasCommit   bool
	HasParams   bool
	Total       int64
	ChunkSize   int64
}

// StagingDir returns the directory that holds staging files for remotePath.
// Staging always lives next to the target, so this is the target's parent
// directory, or "." for a bare relative name.
func StagingDir(remotePath string) string {
	dir := parentDir(remotePath)
	if dir == "" {
		return "."
	}
	return dir
}

// ScanStaging lists the staging directory of remotePath and groups the
// staging files by upload ID. A missing directory yields an empty result so
// begin can target a not-yet-created directory. Results are sorted by upload
// ID, giving adoption a deterministic order. A group whose params file cannot
// be read is reported without params: it can never match, and the reaper
// still sees its files.
func ScanStaging(ctx context.Context, destination base.FileSystem, remotePath string) ([]StagedUpload, error) {
	baseName := remotePath[strings.LastIndexAny(remotePath, `/\`)+1:]
	prefix := "." + baseName + stagingMarker
	entries, err := destination.List(ctx, StagingDir(remotePath))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	groups := map[string]*StagedUpload{}
	for _, entry := range entries {
		if entry.Kind != base.FileFile || !strings.HasPrefix(entry.Name, prefix) {
			continue
		}
		uploadID, kind, ok := ParseStagingName(entry.Name)
		if !ok {
			continue
		}
		group := groups[uploadID]
		if group == nil {
			group = &StagedUpload{UploadID: uploadID, Parts: map[int64]int64{}}
			groups[uploadID] = group
		}
		switch {
		case kind == "manifest":
			group.HasManifest = true
		case kind == "commit":
			group.HasCommit = true
		case kind == "params":
			group.HasParams = true
			total, chunkSize, err := readStagingParams(ctx, destination, entry.Path)
			if err != nil {
				group.HasParams = false
				continue
			}
			group.Total, group.ChunkSize = total, chunkSize
		default:
			index, err := strconv.ParseInt(strings.TrimPrefix(kind, "part-"), 10, 64)
			if err != nil {
				continue
			}
			group.Parts[index] = entry.Size
		}
	}
	result := make([]StagedUpload, 0, len(groups))
	for _, group := range groups {
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UploadID < result[j].UploadID })
	return result, nil
}

// FindStaged scans the staging directory of remotePath for an interrupted
// upload whose recorded parameters match total and chunkSize exactly, and
// returns a reconstructed Upload for it. The caller resumes the returned
// upload to rebuild its staged progress; a false result means begin should
// create a fresh upload ID.
func FindStaged(ctx context.Context, destination base.FileSystem, remotePath string, total int64, chunkSize int) (*Upload, bool, error) {
	staged, err := ScanStaging(ctx, destination, remotePath)
	if err != nil {
		return nil, false, err
	}
	for _, candidate := range staged {
		upload, err := New(candidate.UploadID, remotePath, total, chunkSize)
		if err != nil {
			continue
		}
		if upload.MatchesStaged(candidate) {
			return upload, true, nil
		}
	}
	return nil, false, nil
}

// readStagingParams decodes a staging params file written by prepare.
func readStagingParams(ctx context.Context, destination base.FileSystem, path string) (total, chunkSize int64, err error) {
	reader, err := destination.OpenRead(ctx, path)
	if err != nil {
		return 0, 0, err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if reader.Size() != stagingParamsSize {
		return 0, 0, fmt.Errorf("staging params size is %d, want %d", reader.Size(), stagingParamsSize)
	}
	var raw [stagingParamsSize]byte
	if _, err := io.ReadFull(reader, raw[:]); err != nil {
		return 0, 0, err
	}
	total = int64(binary.LittleEndian.Uint64(raw[0:8]))
	chunkSize = int64(binary.LittleEndian.Uint64(raw[8:16]))
	if total < 0 || chunkSize <= 0 || chunkSize > MaxChunkSize {
		return 0, 0, fmt.Errorf("invalid staging params total=%d chunkSize=%d", total, chunkSize)
	}
	return total, chunkSize, nil
}

// writeStagingParams records the upload's parameters next to its staging
// files so a later begin can tell whether crashed staging matches its own
// total and chunk size before adopting anything.
func (u *Upload) writeStagingParams(ctx context.Context, destination base.FileSystem) (err error) {
	writer, err := destination.OpenWrite(ctx, u.paramsPath(), false)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = writer.Close()
		}
	}()
	var raw [stagingParamsSize]byte
	binary.LittleEndian.PutUint64(raw[0:8], uint64(u.total))
	binary.LittleEndian.PutUint64(raw[8:16], uint64(u.chunkSize))
	if _, err := writeAll(ctx, writer, raw[:]); err != nil {
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
