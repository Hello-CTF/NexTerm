package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Hello-CTF/NexTerm/internal/dbtest"
	"github.com/Hello-CTF/NexTerm/internal/ipc"
	"github.com/Hello-CTF/NexTerm/internal/store"
)

// Keyframes round-trip compressed, list in sequence order, stay with their
// owner and leave with their transcript.
func TestTranscriptKeyframesDatabase(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewFixture(t).OpenStore(t)
	owner := ipc.WithUserID(ctx, "keyframe-owner")
	other := ipc.WithUserID(ctx, "keyframe-other")

	row := store.TranscriptRow{ID: "keyframe-transcript", SessionID: "sess-1", AssetID: "asset-1", StartedAt: 1000}
	if err := db.TranscriptStart(owner, row); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("\x1b[1;32mline\x1b[0m\r\n", 4000)
	for _, frame := range []store.TranscriptKeyframe{
		{NextSeq: 40, TS: 61_000, Cols: 80, Rows: 24, State: big},
		{NextSeq: 7, TS: 2000, Cols: 120, Rows: 40, State: "early"},
	} {
		if err := db.TranscriptKeyframeAdd(owner, row.ID, frame); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := db.TranscriptKeyframes(owner, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 2 || frames[0].NextSeq != 7 || frames[0].State != "early" || frames[0].Cols != 120 ||
		frames[1].NextSeq != 40 || frames[1].State != big || frames[1].TS != 61_000 {
		t.Fatalf("keyframes = %+v", frames)
	}
	if _, err := db.TranscriptKeyframes(other, row.ID); err == nil {
		t.Fatal("another owner read the keyframes")
	}

	if err := db.TranscriptDelete(owner, row.ID); err != nil {
		t.Fatal(err)
	}
	// A transcript deleted meanwhile drops a late keyframe silently.
	if err := db.TranscriptKeyframeAdd(owner, row.ID, store.TranscriptKeyframe{NextSeq: 50, State: "late"}); err != nil {
		t.Fatalf("keyframe for a deleted transcript = %v", err)
	}
	if err := db.TranscriptStart(owner, row); err != nil {
		t.Fatal(err)
	}
	if frames, err := db.TranscriptKeyframes(owner, row.ID); err != nil || len(frames) != 0 {
		t.Fatalf("keyframes survived their transcript: %+v, %v", frames, err)
	}
}
