package chunked

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"

	"github.com/Hello-CTF/NexTerm/internal/transport/base"
)

// ResumeStatus describes the staged state rebuilt from the destination.
type ResumeStatus struct {
	Received int64
	Chunks   []int64
}

// Resume rebuilds upload progress from the destination's staging area so a
// new session can continue an interrupted upload instead of re-sending every
// chunk. Every manifest entry is validated against its staged part; staged
// parts that no longer match are removed so they can be re-sent, and valid
// parts missing from the manifest (a crash between staging and the manifest
// append) are adopted into it.
func (u *Upload) Resume(ctx context.Context, destination base.FileSystem) (ResumeStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.committed || u.aborted {
		return ResumeStatus{}, fmt.Errorf("upload session is closed")
	}
	if err := ctx.Err(); err != nil {
		return ResumeStatus{}, err
	}
	manifested := map[int64]struct{}{}
	if err := u.readManifest(ctx, destination, manifested); err != nil {
		return ResumeStatus{}, err
	}
	buffer := make([]byte, commitBufferSize)
	received := int64(0)
	for index := range manifested {
		if err := u.validateStaged(ctx, destination, index, buffer); err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, errPartMismatch) {
				cleanupPath(u.partPath(index), destination.Delete)
				delete(manifested, index)
				continue
			}
			return ResumeStatus{}, err
		}
		received += u.expectedChunkSize(index)
	}
	for index := int64(0); index < u.chunkCount; index++ {
		if _, ok := manifested[index]; ok {
			continue
		}
		if err := u.validateStaged(ctx, destination, index, buffer); err != nil {
			continue
		}
		if err := appendManifest(ctx, destination, u.manifestPath(), index); err != nil {
			return ResumeStatus{}, err
		}
		manifested[index] = struct{}{}
		received += u.expectedChunkSize(index)
	}
	staged := make([]int64, 0, len(manifested))
	for index := range manifested {
		staged = append(staged, index)
	}
	sort.Slice(staged, func(i, j int) bool { return staged[i] < staged[j] })
	u.received = received
	if len(staged) > 0 {
		u.prepared = true
	}
	return ResumeStatus{Received: received, Chunks: staged}, nil
}

func (u *Upload) validateStaged(ctx context.Context, destination base.FileSystem, index int64, buffer []byte) error {
	if index < 0 || index >= u.chunkCount {
		return fmt.Errorf("chunk index %d out of range", index)
	}
	return readPart(ctx, destination, u.partPath(index), u.expectedChunkSize(index), "", nil, buffer)
}

func (u *Upload) readManifest(ctx context.Context, destination base.FileSystem, into map[int64]struct{}) error {
	manifest, err := destination.OpenRead(ctx, u.manifestPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = manifest.Close() }()
	if manifest.Size() < 0 {
		return fmt.Errorf("invalid upload manifest size %d", manifest.Size())
	}
	var record [8]byte
	for {
		if _, err := io.ReadFull(manifest, record[:]); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil
			}
			return err
		}
		index := int64(binary.LittleEndian.Uint64(record[:]))
		if index < 0 || index >= u.chunkCount {
			continue
		}
		into[index] = struct{}{}
	}
}
