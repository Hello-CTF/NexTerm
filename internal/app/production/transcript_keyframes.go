package production

import (
	"context"
	"log/slog"
	"sync"

	"github.com/Hello-CTF/NexTerm/internal/store"
	"github.com/Hello-CTF/NexTerm/internal/terminal/keyframe"
)

// Seek keyframes are built while a transcript is recorded, at the cadence the
// replay view's own builder uses (transcriptKeyframes.ts): a keyframe after
// the chunk that ends a minute of recorded time or 8 MiB of output since the
// previous one.
const (
	keyframeIntervalMs    = 60_000
	keyframeIntervalBytes = 8 << 20
	// keyframeQueueBytes bounds the output waiting for the keyframe worker;
	// past it, transcripts lose their remaining keyframes instead of slowing
	// the recording.
	keyframeQueueBytes = 32 << 20
	// keyframeMaxScreens bounds the screens modelled at once: each holds up
	// to the replay view's scrollback. Transcripts started past it get no
	// keyframes; the replay view builds them.
	keyframeMaxScreens = 64
	// keyframeDefaultCols and keyframeDefaultRows are xterm's initial size,
	// which the replay view starts from before a transcript's first resize.
	keyframeDefaultCols = 80
	keyframeDefaultRows = 24
)

type keyframeStore interface {
	TranscriptKeyframeAdd(ctx context.Context, transcriptID string, frame store.TranscriptKeyframe) error
}

// keyframeItem is a stored chunk for a transcript's screen, or a control
// item: end releases the screen, broken stops its keyframes.
type keyframeItem struct {
	transcriptID string
	writeCtx     context.Context
	chunk        store.TranscriptChunkRow
	end          bool
	broken       bool
}

type keyframeScreen struct {
	screen      *keyframe.Screen
	nextSeq     int64
	lastFrameTs int64
	bytesSince  int64
}

// transcriptKeyframer models the replay terminal of every transcript being
// recorded from the chunks the writer stored, in order, on its own goroutine.
// A gap in a transcript's chunks (a dropped item or a failed write) ends its
// keyframes; the ones already stored stay valid.
type transcriptKeyframer struct {
	database      keyframeStore
	logger        *slog.Logger
	intervalBytes int64

	mu          sync.Mutex
	queue       []keyframeItem
	queuedBytes int
	closed      bool
	wake        chan struct{}
	done        chan struct{}

	screens map[string]*keyframeScreen
	stopped map[string]bool
}

func newTranscriptKeyframer(database keyframeStore, logger *slog.Logger) *transcriptKeyframer {
	return &transcriptKeyframer{
		database: database, logger: logger, intervalBytes: keyframeIntervalBytes,
		wake: make(chan struct{}, 1), done: make(chan struct{}),
		screens: map[string]*keyframeScreen{}, stopped: map[string]bool{},
	}
}

func (k *transcriptKeyframer) start() {
	go func() {
		defer close(k.done)
		k.run()
	}()
}

// close takes no more items and waits until the worker built the keyframes
// of the queued ones, or until ctx ends.
func (k *transcriptKeyframer) close(ctx context.Context) {
	k.mu.Lock()
	k.closed = true
	k.mu.Unlock()
	k.signal()
	select {
	case <-k.done:
	case <-ctx.Done():
	}
}

func (k *transcriptKeyframer) signal() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// chunks hands over the chunks just stored for a transcript. When the queue
// is full they are dropped, which the worker sees as a gap.
func (k *transcriptKeyframer) chunks(writeCtx context.Context, transcriptID string, rows []store.TranscriptChunkRow) {
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return
	}
	for _, row := range rows {
		if k.queuedBytes+len(row.Data) > keyframeQueueBytes {
			break
		}
		k.queuedBytes += len(row.Data)
		k.queue = append(k.queue, keyframeItem{transcriptID: transcriptID, writeCtx: writeCtx, chunk: row})
	}
	k.mu.Unlock()
	k.signal()
}

// control queues an end or broken item; control items are never dropped.
func (k *transcriptKeyframer) control(transcriptID string, end bool) {
	k.mu.Lock()
	if !k.closed {
		k.queue = append(k.queue, keyframeItem{transcriptID: transcriptID, end: end, broken: !end})
	}
	k.mu.Unlock()
	k.signal()
}

func (k *transcriptKeyframer) run() {
	for {
		k.mu.Lock()
		batch := k.queue
		k.queue = nil
		k.queuedBytes = 0
		closed := k.closed
		k.mu.Unlock()
		if len(batch) == 0 {
			if closed {
				return
			}
			<-k.wake
			continue
		}
		for _, item := range batch {
			k.apply(item)
		}
	}
}

func (k *transcriptKeyframer) apply(item keyframeItem) {
	id := item.transcriptID
	if item.end {
		delete(k.screens, id)
		delete(k.stopped, id)
		return
	}
	if k.stopped[id] {
		return
	}
	if item.broken {
		k.stop(id)
		return
	}
	state := k.screens[id]
	if state == nil {
		if item.chunk.Seq != 0 || len(k.screens) >= keyframeMaxScreens {
			k.stop(id)
			return
		}
		state = &keyframeScreen{screen: keyframe.New(keyframeDefaultCols, keyframeDefaultRows), lastFrameTs: item.chunk.TS}
		k.screens[id] = state
	}
	if item.chunk.Seq != state.nextSeq {
		k.stop(id)
		return
	}
	state.nextSeq++
	switch item.chunk.Kind {
	case store.TranscriptChunkKindOutput:
		state.screen.Write(item.chunk.Data)
		state.bytesSince += int64(len(item.chunk.Data))
	case store.TranscriptChunkKindResize:
		if cols, rows, ok := parseCastResize(item.chunk.Data); ok {
			state.screen.Resize(cols, rows)
		}
	}
	if item.chunk.TS-state.lastFrameTs < keyframeIntervalMs && state.bytesSince < k.intervalBytes {
		return
	}
	cols, rows := state.screen.Size()
	frame := store.TranscriptKeyframe{NextSeq: state.nextSeq, TS: item.chunk.TS, Cols: cols, Rows: rows, State: state.screen.State()}
	state.lastFrameTs = item.chunk.TS
	state.bytesSince = 0
	if err := k.database.TranscriptKeyframeAdd(item.writeCtx, id, frame); err != nil {
		k.logger.Warn("transcript keyframe write failed", "transcriptId", id, "nextSeq", frame.NextSeq, "error", err)
		k.stop(id)
	}
}

func (k *transcriptKeyframer) stop(id string) {
	delete(k.screens, id)
	k.stopped[id] = true
}
