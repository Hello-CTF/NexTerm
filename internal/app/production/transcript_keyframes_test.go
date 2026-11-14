package production

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/terminal/keyframe"
)

// Keyframes are built from the stored chunks while the transcript records:
// each one carries the screen after every chunk before NextSeq, which the
// replay view restores instead of replaying from the start.
func TestTranscriptWriterBuildsKeyframesWhileRecording(t *testing.T) {
	ctx := context.Background()
	database, err := store.OpenInMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	writer := startTestWriter(t, database, transcriptWriterConfig{KeyframeIntervalBytes: 64})
	// The row is created lazily with the first output; its first chunk is
	// still the starting geometry at seq 0, which the keyframes build on.
	writer.RecordingStarted(ctx, transcriptIdentity("session-keyframes", "tab-1"), 40, 5, nil)
	var replay []string
	for i := 0; i < 20; i++ {
		line := "\x1b[1;32mline\x1b[0m " + strings.Repeat("x", i) + "\r\n"
		replay = append(replay, line)
		writer.RecordingOutput(ctx, "session-keyframes", "tab-1", []byte(line))
	}
	writer.RecordingEnded(ctx, "session-keyframes", "tab-1")
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := writer.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}

	rows, err := database.TranscriptListByAsset(ctx, "asset-1", 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("transcripts = %+v, %v", rows, err)
	}
	frames, err := database.TranscriptKeyframes(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 3 {
		t.Fatalf("keyframes = %d, want one after every 64 bytes of output", len(frames))
	}
	for i, frame := range frames {
		// Chunk 0 is the resize; chunk n (n >= 1) is output line n-1.
		expected := keyframe.New(80, 24)
		expected.Resize(40, 5)
		for _, line := range replay[:frame.NextSeq-1] {
			expected.Write([]byte(line))
		}
		if frame.Cols != 40 || frame.Rows != 5 || frame.State != expected.State() {
			t.Fatalf("keyframe %d (next seq %d) differs from the screen after its chunks", i, frame.NextSeq)
		}
		if i > 0 && frame.NextSeq <= frames[i-1].NextSeq {
			t.Fatalf("keyframes out of order: %d after %d", frame.NextSeq, frames[i-1].NextSeq)
		}
	}
}

// A gap in a transcript's chunks ends its keyframes: a screen missing chunks
// would not match the replay.
func TestTranscriptKeyframerStopsAtAGap(t *testing.T) {
	recorder := &keyframeRecorder{}
	keyframer := newTranscriptKeyframer(recorder, discardTranscriptLogger())
	keyframer.intervalBytes = 1
	chunk := func(seq int64) store.TranscriptChunkRow {
		return store.TranscriptChunkRow{Seq: seq, TS: seq, Kind: store.TranscriptChunkKindOutput, Data: []byte("x")}
	}
	ctx := context.Background()
	for _, item := range []keyframeItem{
		{transcriptID: "t", writeCtx: ctx, chunk: chunk(0)},
		{transcriptID: "t", writeCtx: ctx, chunk: chunk(1)},
		{transcriptID: "t", writeCtx: ctx, chunk: chunk(3)},
		{transcriptID: "t", writeCtx: ctx, chunk: chunk(4)},
	} {
		keyframer.apply(item)
	}
	if got := recorder.nextSeqs(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("keyframes = %v, want the two before the gap", got)
	}
	keyframer.apply(keyframeItem{transcriptID: "t", end: true})
	if len(keyframer.screens) != 0 || len(keyframer.stopped) != 0 {
		t.Fatal("the end of the transcript must release its state")
	}
}

type keyframeRecorder struct {
	frames []store.TranscriptKeyframe
}

func (r *keyframeRecorder) TranscriptKeyframeAdd(_ context.Context, _ string, frame store.TranscriptKeyframe) error {
	r.frames = append(r.frames, frame)
	return nil
}

func (r *keyframeRecorder) nextSeqs() []int64 {
	var out []int64
	for _, frame := range r.frames {
		out = append(out, frame.NextSeq)
	}
	return out
}
