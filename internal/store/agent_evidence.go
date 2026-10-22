package store

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/ids"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
)

type AgentEvidenceKeyRow = model.AgentEvidenceKeyRow
type AgentEvidenceAnchorRow = model.AgentEvidenceAnchorRow

// ErrEvidenceAnchorNotFound reports a capture with no trusted anchor for the
// presented chain head: evidence operations must refuse, not re-issue.
var ErrEvidenceAnchorNotFound = errors.New("evidence anchor 不存在")

const (
	// evidenceWinnerReadAttempts bounds how often the winner read retries
	// after a lost creation race. SQLite can surface the unique constraint
	// before the winner's insert is committed and therefore visible, so an
	// immediate re-read may still miss the winner's row.
	evidenceWinnerReadAttempts = 8
	evidenceWinnerReadBackoff  = 25 * time.Millisecond
)

// inEvidenceWinnerRead runs read, retrying a not-found outcome a bounded
// number of times with a context-aware backoff: after a lost unique-insert
// race the winner's transaction may not be visible yet when the constraint
// surfaces, and failing the caller would drop its capture or abort its
// operation. A read that keeps missing after the bounded attempts returns
// the not-found error instead of guessing.
func (s *Store) inEvidenceWinnerRead(ctx context.Context, read func() error) error {
	for attempt := 0; ; attempt++ {
		err := read()
		if !isNoRows(err) {
			return err
		}
		if attempt == evidenceWinnerReadAttempts {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(evidenceWinnerReadBackoff):
		}
	}
}

// EvidenceSignerKey loads-or-creates the per-owner signer key. The key is
// created once and persisted, so anchors issued before a restart still
// verify afterwards; a lost creation race returns the winner's key once the
// winner's insert becomes visible.
func (s *Store) EvidenceSignerKey(ctx context.Context) ([]byte, error) {
	owner := ipc.OwnerID(ctx)
	var row AgentEvidenceKeyRow
	err := s.db.WithContext(ctx).Model(&AgentEvidenceKeyRow{}).Where("owner_id = ?", owner).First(&row).Error
	if err == nil {
		return row.Key, nil
	}
	if !isNoRows(err) {
		return nil, dbError(err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, dbError(err)
	}
	now := ids.NowMS()
	row = AgentEvidenceKeyRow{ID: ids.New(), OwnerID: owner, Key: key, CreatedAt: now, UpdatedAt: now}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if IsUniqueErr(err) {
			// Read the winner into a fresh row: reusing the attempted row
			// would scope the query by its generated primary key and miss
			// the winner's differently-keyed row. The bounded retry covers
			// the window where the winner's commit is not visible yet.
			var winner AgentEvidenceKeyRow
			if err := s.inEvidenceWinnerRead(ctx, func() error {
				return s.db.WithContext(ctx).Model(&AgentEvidenceKeyRow{}).Where("owner_id = ?", owner).First(&winner).Error
			}); err != nil {
				return nil, dbError(err)
			}
			return winner.Key, nil
		}
		return nil, dbError(err)
	}
	return key, nil
}

// PutEvidenceAnchor persists an anchor issued by the trusted recorder. The
// (owner, capture, chain head) key makes the write idempotent: re-putting
// the identical anchor is a no-op, while a different envelope or MAC under
// the same key is a conflict, never a silent success.
func (s *Store) PutEvidenceAnchor(ctx context.Context, captureID, chainHead, envelopeHash, mac string) error {
	row := AgentEvidenceAnchorRow{
		CaptureID: captureID, ChainHead: chainHead, EnvelopeHash: envelopeHash, MAC: mac,
		CreatedAt: ids.NowMS(), OwnerID: ipc.OwnerID(ctx),
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if IsUniqueErr(err) {
			var stored AgentEvidenceAnchorRow
			if err := s.inEvidenceWinnerRead(ctx, func() error {
				return s.db.WithContext(ctx).Model(&AgentEvidenceAnchorRow{}).
					Where("owner_id = ? AND capture_id = ? AND chain_head = ?", row.OwnerID, row.CaptureID, row.ChainHead).
					First(&stored).Error
			}); err != nil {
				return dbError(err)
			}
			if stored.EnvelopeHash == row.EnvelopeHash && stored.MAC == row.MAC {
				return nil
			}
			return ipc.NewError(ipc.Code("conflict"), "evidence anchor 与已存记录冲突")
		}
		return dbError(err)
	}
	return nil
}

// EvidenceAnchor resolves the trusted anchor for the exact (capture, chain
// head) pair; any other combination reports ErrEvidenceAnchorNotFound.
func (s *Store) EvidenceAnchor(ctx context.Context, captureID, chainHead string) (envelopeHash, mac string, err error) {
	var row AgentEvidenceAnchorRow
	err = s.db.WithContext(ctx).Model(&AgentEvidenceAnchorRow{}).
		Where("owner_id = ? AND capture_id = ? AND chain_head = ?", ipc.OwnerID(ctx), captureID, chainHead).
		First(&row).Error
	if isNoRows(err) {
		return "", "", ErrEvidenceAnchorNotFound
	}
	if err != nil {
		return "", "", dbError(err)
	}
	return row.EnvelopeHash, row.MAC, nil
}
