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

// stagingParamsSizeV1 is the on-disk size of the legacy staging params file:
// the upload total and chunk size as little-endian int64 values, with no
// version and no owner.
const stagingParamsSizeV1 = 16

// stagingParamsVersion1 identifies legacy 16-byte params without an owner;
// stagingParamsVersion2 identifies owner-bound params.
const (
	stagingParamsVersion1 = 1
	stagingParamsVersion2 = 2
)

// stagingParamsHeaderV2 is the fixed part of a version 2 params file:
// version byte, total, chunk size, and owner length. A version 2 file is at
// most stagingParamsMaxSizeV2 bytes; anything else fails closed before any
// allocation sized by the remote declaration.
const (
	stagingParamsHeaderV2  = 1 + 8 + 8 + 2
	stagingParamsMaxOwner  = 1<<16 - 1
	stagingParamsMaxSizeV2 = stagingParamsHeaderV2 + stagingParamsMaxOwner
)

// StagedUpload describes one chunked-upload staging group discovered on a
// destination: the staged parts with their sizes, plus which control files
// (params, manifest, commit temporary) survived the crash.
type StagedUpload struct {
	UploadID      string
	Parts         map[int64]int64
	HasManifest   bool
	HasCommit     bool
	HasParams     bool
	ParamsVersion int
	Owner         string
	Total         int64
	ChunkSize     int64
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
			total, chunkSize, owner, version, err := readStagingParams(ctx, destination, entry.Path)
			if err != nil {
				group.HasParams = false
				continue
			}
			group.Total, group.ChunkSize, group.Owner, group.ParamsVersion = total, chunkSize, owner, version
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
// upload whose recorded owner, parameters, and part sizes match exactly, and
// returns a reconstructed Upload for it. The caller resumes the returned
// upload to rebuild its staged progress; a false result means begin should
// create a fresh upload ID.
func FindStaged(ctx context.Context, destination base.FileSystem, remotePath string, total int64, chunkSize int, owner string) (*Upload, bool, error) {
	staged, err := ScanStaging(ctx, destination, remotePath)
	if err != nil {
		return nil, false, err
	}
	for _, candidate := range staged {
		upload, err := New(candidate.UploadID, remotePath, total, chunkSize, WithOwner(owner))
		if err != nil {
			continue
		}
		if upload.MatchesStaged(candidate) {
			return upload, true, nil
		}
	}
	return nil, false, nil
}

// readStagingParams decodes a staging params file written by prepare. Legacy
// 16-byte files carry no owner and report version 1.
func readStagingParams(ctx context.Context, destination base.FileSystem, path string) (total, chunkSize int64, owner string, version int, err error) {
	reader, err := destination.OpenRead(ctx, path)
	if err != nil {
		return 0, 0, "", 0, err
	}
	defer func() {
		if closeErr := reader.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	size := reader.Size()
	if size != stagingParamsSizeV1 && (size < stagingParamsHeaderV2 || size > stagingParamsMaxSizeV2) {
		return 0, 0, "", 0, fmt.Errorf("staging params size %d is invalid", size)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(reader, raw); err != nil {
		return 0, 0, "", 0, err
	}
	if size == stagingParamsSizeV1 {
		total, chunkSize, err = decodeStagingParamsV1(raw)
		return total, chunkSize, "", stagingParamsVersion1, err
	}
	if raw[0] != stagingParamsVersion2 {
		return 0, 0, "", 0, fmt.Errorf("staging params version %d is unsupported", raw[0])
	}
	ownerLen := int(binary.LittleEndian.Uint16(raw[17:19]))
	if ownerLen < 0 || stagingParamsHeaderV2+ownerLen != len(raw) {
		return 0, 0, "", 0, fmt.Errorf("staging params owner length %d is invalid", ownerLen)
	}
	total = int64(binary.LittleEndian.Uint64(raw[1:9]))
	chunkSize = int64(binary.LittleEndian.Uint64(raw[9:17]))
	if total < 0 || chunkSize <= 0 || chunkSize > MaxChunkSize {
		return 0, 0, "", 0, fmt.Errorf("invalid staging params total=%d chunkSize=%d", total, chunkSize)
	}
	return total, chunkSize, string(raw[stagingParamsHeaderV2:]), stagingParamsVersion2, nil
}

func decodeStagingParamsV1(raw []byte) (total, chunkSize int64, err error) {
	total = int64(binary.LittleEndian.Uint64(raw[0:8]))
	chunkSize = int64(binary.LittleEndian.Uint64(raw[8:16]))
	if total < 0 || chunkSize <= 0 || chunkSize > MaxChunkSize {
		return 0, 0, fmt.Errorf("invalid staging params total=%d chunkSize=%d", total, chunkSize)
	}
	return total, chunkSize, nil
}

// writeStagingParams records the upload's owner and parameters next to its
// staging files so a later begin can tell whether crashed staging matches
// its own identity before adopting anything.
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
	raw := make([]byte, stagingParamsHeaderV2+len(u.owner))
	raw[0] = stagingParamsVersion2
	binary.LittleEndian.PutUint64(raw[1:9], uint64(u.total))
	binary.LittleEndian.PutUint64(raw[9:17], uint64(u.chunkSize))
	binary.LittleEndian.PutUint16(raw[17:19], uint16(len(u.owner)))
	copy(raw[stagingParamsHeaderV2:], u.owner)
	if _, err := writeAll(ctx, writer, raw); err != nil {
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
