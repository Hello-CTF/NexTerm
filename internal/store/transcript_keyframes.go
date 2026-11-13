package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/klauspost/compress/zstd"
	"gorm.io/gorm"

	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store/model"
	"github.com/Hello-CTF/NexTerm/internal/store/query"
)

type TranscriptKeyframeRow = model.TranscriptKeyframe

// TranscriptKeyframe is a decoded seek keyframe.
type TranscriptKeyframe struct {
	NextSeq int64
	TS      int64
	Cols    int
	Rows    int
	State   string
}

// maxKeyframeState bounds a decoded keyframe: 5000 lines of scrollback with
// renditions stay far below it.
const maxKeyframeState = 64 << 20

var (
	keyframeEncoder, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
	keyframeDecoder, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxMemory(maxKeyframeState))
)

// TranscriptKeyframeAdd stores a keyframe of a transcript the caller owns.
// A transcript deleted meanwhile is not an error: its keyframe is dropped.
func (s *Store) TranscriptKeyframeAdd(ctx context.Context, transcriptID string, frame TranscriptKeyframe) error {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		var appErr *ipc.Error
		if errors.As(err, &appErr) && appErr.Code == ipc.CodeNotFound {
			return nil
		}
		return err
	}
	row := TranscriptKeyframeRow{
		TranscriptID: transcriptID, NextSeq: frame.NextSeq, TS: frame.TS, Cols: frame.Cols, Rows: frame.Rows,
		State: keyframeEncoder.EncodeAll([]byte(frame.State), nil), OwnerID: ipc.OwnerID(ctx),
	}
	if err := query.Use(s.db).TranscriptKeyframe.WithContext(ctx).Create(&row); err != nil {
		return dbError(err)
	}
	return nil
}

// TranscriptKeyframes returns the keyframes of a transcript in sequence
// order.
func (s *Store) TranscriptKeyframes(ctx context.Context, transcriptID string) ([]TranscriptKeyframe, error) {
	if _, err := s.TranscriptGet(ctx, transcriptID); err != nil {
		return nil, err
	}
	keyframe := query.Use(s.db).TranscriptKeyframe
	var rows []TranscriptKeyframeRow
	if err := keyframe.WithContext(ctx).
		Where(keyframe.TranscriptID.Eq(transcriptID)).
		Where(ownerConditions(ctx, keyframe.TableName())...).
		Order(keyframe.NextSeq).
		Scan(&rows); err != nil {
		return nil, dbError(err)
	}
	frames := make([]TranscriptKeyframe, 0, len(rows))
	for _, row := range rows {
		state, err := keyframeDecoder.DecodeAll(row.State, nil)
		if err != nil {
			return nil, dbError(fmt.Errorf("decode keyframe %d of transcript %s: %w", row.NextSeq, transcriptID, err))
		}
		frames = append(frames, TranscriptKeyframe{NextSeq: row.NextSeq, TS: row.TS, Cols: row.Cols, Rows: row.Rows, State: string(state)})
	}
	return frames, nil
}

// deleteTranscriptKeyframesTx drops the keyframes of replaced content.
func deleteTranscriptKeyframesTx(ctx context.Context, tx *gorm.DB, transcriptID string) error {
	return tx.WithContext(ctx).Where("transcript_id = ?", transcriptID).Delete(&model.TranscriptKeyframe{}).Error
}
